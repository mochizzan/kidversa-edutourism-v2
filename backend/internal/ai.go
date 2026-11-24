package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const geminiTimeout = 15 * time.Second // 05 §5 Gemini statement budget

// GeminiBaseURL is the Gemini endpoint prefix. It is a package var (not a
// const) as the integration suite's test seam: those tests point it at a
// local stub server to exercise AI_FAILED without network egress.
var GeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"

// generateNarrative renders prompts/system.md + prompts/user.md with {{Var}}
// replacement (ChildName/ChildAge/SessionName/TopicName/SessionDate/
// Assessments, 05 §4 — no HTML escaping) and makes ONE Gemini generateContent
// call (15 s). Manual retry only — no auto-loop (06 §3); the caller maps any
// error to AI_FAILED and keeps the previous narrative.
func (s *Server) generateNarrative(childName string, childAge int, sessionName, topicName, sessionDate, assessments string) (string, error) {
	user := renderVars(s.userPrompt, map[string]string{
		"ChildName":   childName,
		"ChildAge":    strconv.Itoa(childAge),
		"SessionName": sessionName,
		"TopicName":   topicName,
		"SessionDate": sessionDate,
		"Assessments": assessments,
	})
	prompt := strings.TrimSpace(s.sysPrompt + "\n\n" + user)

	payload, err := json.Marshal(map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]string{{"text": prompt}}},
		},
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), geminiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		GeminiBaseURL+"/"+s.cfg.GeminiModel+":generateContent", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", s.cfg.GeminiAPIKey)

	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini http %d: %.200s", resp.StatusCode, body)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini returned no candidates")
	}
	text := strings.TrimSpace(out.Candidates[0].Content.Parts[0].Text)
	if text == "" {
		return "", fmt.Errorf("gemini returned empty text")
	}
	return text, nil
}

// renderVars replaces every {{Var}} occurrence (05 §4).
func renderVars(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl
}
