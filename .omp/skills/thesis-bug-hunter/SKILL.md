---

name: thesis-bug-hunter
description: Orchestrate a persistent five-agent thesis defense and adversarial bug-hunting workflow using exactly two persistent Bug Hunters and three persistent Validators. Create a dedicated execution folder directly under ./docs/superskill/ containing exactly four persistent artifacts: an initial thesis/context document shared with all five agents, one Bug Hunter workspace shared by both Bug Hunters, one Validator workspace shared by all three Validators, and one final report containing agreed findings and recommendations. Use when auditing, challenging, testing, defending, or verifying a thesis, research document, software project, implementation, or other codebase-related work. When the user provides little or no context, automatically perform broad and systematic investigation across business logic, functional correctness, error handling, edge cases, state transitions, validation, data integrity, security, concurrency, failure recovery, architecture, and other relevant behavior.
----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

# Thesis Bug Hunter & Defense Orchestrator

## Mission

Act as the **Main Orchestrator, Moderator, Evidence Gatekeeper, and Final Judge** for a persistent adversarial thesis and software investigation.

The workflow MUST use exactly:

* **2 persistent Bug Hunter agents**
* **3 persistent Validator / Examiner agents**
* **1 Main Agent acting as orchestrator**

The five subagents are persistent participants throughout the entire execution.

Do NOT repeatedly create replacement agents for follow-up questions.

Do NOT create additional Bug Hunters or Validators merely because a later question appears.

The objective is not to maximize the number of reported bugs.

The objective is to determine, with evidence:

1. What is actually wrong.
2. Why it is a bug.
3. What the correct expected behavior should be.
4. Whether the finding is actually a bug or merely a design choice, limitation, requirement ambiguity, or misunderstood behavior.
5. Whether the finding survives adversarial examination.
6. What should be corrected, documented, or defended.

The final result must distinguish genuine defects from false positives.

---

# 1. Artifact Architecture

Every execution MUST create a dedicated workspace directly under:

```text
./docs/superskill/
```

The workspace MUST NOT be created under:

```text
./docs/superskill/task/
```

A recommended structure is:

```text
./docs/superskill/thesis-bug-hunter/<execution-folder>/
```

The execution folder SHOULD follow:

```text
YYYY-MM-DD-<task-name>/
```

Example:

```text
./docs/superskill/thesis-bug-hunter/2026-10-07-thesis-defense/
```

Inside that execution folder there MUST be exactly **four persistent artifacts**:

```text
2026-10-07-thesis-defense/
├── 01-initial-thesis.md
├── 02-bug-hunter.md
├── 03-validator.md
└── 04-final-report.md
```

No additional persistent report, findings, evidence, summary, or task-state artifact should be created inside this execution folder unless the user explicitly changes this requirement.

Temporary runtime outputs may exist outside the four canonical artifacts when required by the tooling, but they are not part of the persistent investigation artifact set.

---

# 2. The Four Persistent Artifacts

## 2.1 `01-initial-thesis.md`

This is the initial thesis/context document created by the Main Agent.

It serves as the shared baseline for all five subagents.

It must contain the relevant context that the Main Agent has established before delegation.

The exact same logical context MUST be distributed to:

* Bug Hunter A
* Bug Hunter B
* Validator A
* Validator B
* Validator C

The Main Agent MUST NOT create five separate versions of the initial context.

Before delegation, the Main Agent MUST:

1. Create the initial thesis file.
2. Read it again.
3. Critically review it.
4. Identify missing context, unsupported assumptions, contradictions, and ambiguity.
5. Edit it.
6. Read it again.
7. Repeat the review/correction cycle until sufficiently complete.
8. Only then distribute it to the five agents.

The file is the shared baseline, not the final verdict.

---

## 2.2 `02-bug-hunter.md`

This is the single persistent Bug Hunter workspace shared by:

* Bug Hunter A
* Bug Hunter B

Both Bug Hunters MUST use the same artifact contextually, but their responsibilities MUST remain separated into explicit sections.

The document MUST contain:

* Global investigation context
* Bug Hunter A section
* Bug Hunter B section
* Cross-hunter discussion
* Finding sections
* Defense rounds
* Disputes
* Retractions
* Reclassifications
* Open questions
* Main Agent review state

The file MUST be structured so that each finding has a reusable section template and each challenge/response cycle has a reusable round template.

---

## 2.3 `03-validator.md`

This is the single persistent Validator workspace shared by:

* Validator A
* Validator B
* Validator C

All three Validators use the same artifact but maintain clearly separated perspectives.

The document MUST contain:

* Validator A section
* Validator B section
* Validator C section
* Finding-by-finding examination
* Questions
* Evidence requests
* Bug Hunter responses
* Counterarguments
* Validator verdicts
* Remaining objections
* Agreement/disagreement tracking
* Examination rounds

It must also contain reusable section and round templates.

---

## 2.4 `04-final-report.md`

This is the final consolidated report.

It MUST be created as the final persistent artifact during the execution setup, but its substantive content MUST only be finalized after the investigation and examination phases are complete.

It contains:

* Final findings
* Confirmed bugs
* Partially confirmed findings
* Rejected findings
* Non-bug classifications
* Unresolved findings
* Strongest evidence
* Strongest objections
* Final reasoning
* Recommendations
* Thesis-defense readiness
* Remaining risks
* Final conclusion

The final report MUST NOT simply copy every provisional claim.

Only findings that survive the adjudication process belong in the confirmed section.

---

# 3. Main Agent Responsibilities

The Main Agent is the sole Orchestrator.

Its responsibilities are:

1. Understand the user's request.
2. Inspect the available repository/project context.
3. Create the execution workspace.
4. Create the four artifact files.
5. Review and improve the initial thesis artifact.
6. Distribute the shared context.
7. Spawn and coordinate the five persistent subagents.
8. Maintain communication between teams.
9. Review all findings.
10. Challenge weak findings itself.
11. Resolve contradictory evidence.
12. Update the Bug Hunter artifact.
13. Update the Validator artifact.
14. Determine whether findings are actually bugs.
15. Decide when evidence is sufficient.
16. Produce the final report.
17. Validate the final report against the actual investigation.

The Main Agent is NOT an additional Bug Hunter.

The Main Agent is NOT an additional Validator.

The Main Agent must avoid duplicating specialist work unnecessarily.

---

# 4. Single Source of Orchestration Truth

The four artifacts have different roles.

```text
01-initial-thesis.md
    ↓
Shared baseline / investigation context

02-bug-hunter.md
    ↓
Bug Hunter investigation + defense state

03-validator.md
    ↓
Adversarial examination + verdict state

04-final-report.md
    ↓
Final adjudicated result + recommendations
```

Do not use the final report as live scratch space.

Do not use the initial thesis file as a findings log.

Do not use the Bug Hunter file as the Validator's permanent state.

Do not use the Validator file as a substitute for the final report.

Maintain clear separation of responsibilities.

---

# 5. Dynamic Configuration

Resolve the following from the user request, repository, thesis, project documentation, or available context:

* Project
* Thesis/document
* Thesis template
* Repository
* Scope
* Research topic
* Methodology
* Technology stack
* Architecture
* Domain/business context
* Expected behavior
* Evidence requirements
* Defense standard
* Completion condition
* Language

Never invent project-specific facts.

If a value cannot be established, use:

```text
<not-provided>
```

Do not manufacture a requirement simply to make the workflow easier.

---

# 6. No-Context Default Mode

If the user provides little or no useful context when invoking the skill, do NOT perform a shallow review.

The skill MUST default to a broad and systematic investigation.

At minimum, Bug Hunters must actively consider:

* Business logic
* Functional behavior
* Input validation
* Output correctness
* State transitions
* Data flow
* Error handling
* Exception handling
* Edge cases
* Boundary conditions
* Null / empty / missing values
* Invalid input
* Unexpected input
* Authentication
* Authorization
* Permissions
* Data integrity
* Persistence behavior
* Transaction boundaries
* Concurrency
* Race conditions
* Idempotency
* Retries
* Timeout behavior
* External dependency failures
* API failures
* Network failures
* Partial failures
* Recovery behavior
* Resource lifecycle
* Memory/resource leaks
* Configuration behavior
* Environment assumptions
* Platform differences
* Time/date handling
* Timezone behavior
* Ordering assumptions
* Caching
* Consistency
* Security boundaries
* Sensitive data handling
* Logging and observability
* Performance-sensitive logic
* Regression risks
* Backward compatibility
* Architecture violations
* Incorrect abstraction
* Dead or unreachable logic
* Inconsistent error semantics
* Incorrect defaults
* Incomplete feature states
* Missing fallback behavior
* Unexpected user flows
* Failure recovery
* Integration boundaries
* Test coverage gaps where they materially reveal risk
* Thesis claims that do not match the implementation
* Thesis claims that lack sufficient evidence
* Methodology inconsistencies
* Research limitations
* Requirement-to-implementation mismatches

This list is not exhaustive.

The Bug Hunters SHOULD follow the actual domain and expand the audit surface where the codebase reveals additional risk.

A no-context execution means:

> **Investigate broadly first, then narrow based on evidence.**

It does NOT mean:

> assume everything is broken.

---

# 7. Required Agent Roster

Exactly five persistent subagents must be used.

## Bug Hunter A

Primary perspective:

**Technical and behavioral correctness**

Focus on:

* implementation behavior
* business logic
* control flow
* state transitions
* data handling
* errors
* edge cases
* integration behavior
* architectural implications

Bug Hunter A should actively attempt to discover real defects.

---

## Bug Hunter B

Primary perspective:

**Independent adversarial reasoning**

Focus on a different attack surface from Bug Hunter A, such as:

* requirements correctness
* business/domain interpretation
* thesis consistency
* methodological implications
* hidden assumptions
* correctness under unusual conditions
* defense weaknesses
* alternative explanations

Bug Hunter B MUST independently challenge Bug Hunter A when appropriate.

The two Bug Hunters MUST NOT simply duplicate each other's reasoning.

---

## Validator A

Act as a hostile-but-fair examiner focused primarily on technical validity.

---

## Validator B

Act as a hostile-but-fair examiner focused primarily on requirements, business logic, and behavioral correctness.

---

## Validator C

Act as a hostile-but-fair examiner focused primarily on academic defensibility, evidence quality, methodology, and alternative explanations.

These focus areas may be adapted when the actual project requires something different.

---

# 8. Persistence Rule

The five subagents are persistent.

Do NOT:

* replace an agent because it asked a question;
* create another Bug Hunter for follow-up;
* create another Validator to break a tie;
* restart an agent merely to obtain a second opinion.

Follow-up questions MUST be sent to the existing agent.

The value of the architecture is continuity of reasoning.

---

# 9. Phase 0 — Artifact Initialization

The Main Agent MUST initialize the execution workspace before spawning the five agents.

Required sequence:

```text
Determine execution name
        ↓
Create execution folder
        ↓
Create exactly four artifacts
        ↓
Populate 01-initial-thesis.md
        ↓
Read 01-initial-thesis.md
        ↓
Critically review
        ↓
Correct
        ↓
Read again
        ↓
Repeat until sufficiently complete
        ↓
Initialize templates in 02 / 03 / 04
        ↓
Spawn five persistent agents
```

The Main Agent MUST NOT create the artifacts and immediately delegate without reviewing the initial thesis context.

---

# 10. Initial Thesis Artifact Template

`01-initial-thesis.md` SHOULD contain:

```md
# Initial Thesis Context

## 1. Investigation Identity

- Execution:
- Date:
- Project:
- Thesis:
- Repository:
- Scope:
- Research Topic:
- Methodology:
- Technology Stack:
- Defense Standard:

---

## 2. Problem / Objective

...

---

## 3. Established Context

### Project Context

...

### Thesis Context

...

### Domain Context

...

### Technical Context

...

---

## 4. Expected Behavior

Document only behavior that can be established from:

- explicit requirements
- thesis statements
- project specifications
- accepted domain rules
- verified architectural intent
- observable invariants

---

## 5. Constraints

...

---

## 6. Known Evidence

...

---

## 7. Known Uncertainties

...

---

## 8. Investigation Scope

### Primary Areas

- ...

### Secondary Areas

- ...

### Areas Requiring Special Attention

- ...

---

## 9. Investigation Standard

The investigation must distinguish:

- Bug
- Design choice
- Limitation
- Requirement ambiguity
- Missing requirement
- Documentation problem
- Thesis claim problem
- Environment-specific behavior
- False positive
- Unresolved issue

---

## 10. Initial Working Hypotheses

...

---

## 11. Main Agent Notes

...

```

The Main Agent MUST correct this document before distributing it.

---

# 11. Shared Context Distribution

After `01-initial-thesis.md` is finalized, the Main Agent MUST provide its context to all five subagents.

All five agents must receive the same baseline.

The Main Agent SHOULD provide the artifact path and relevant contents/context according to the capabilities of the agent runtime.

No agent should silently construct an incompatible baseline.

---

# 12. Bug Hunter Workspace

`02-bug-hunter.md` MUST provide one shared persistent workspace for both Bug Hunters.

Recommended structure:

```md
# Bug Hunter Workspace

## Investigation Status

- Overall Status:
- Current Round:
- Active Focus:
- Open Findings:
- Findings Awaiting Verification:

---

# Bug Hunter A

## Agent Profile

- Name:
- Perspective:
- Responsibility:
- Status:

## Investigation Scope

...

## Findings

### Finding Template

#### <FINDING-ID> — <TITLE>

- Hunter:
- Category:
- Severity:
- Location:
- Claim:
- Why This Appears To Be A Bug:
- Expected Behavior:
- Actual Behavior:
- Evidence:
- Reproduction / Verification:
- Impact:
- Business Impact:
- Thesis Impact:
- Confidence:
- Alternative Explanation:
- Current Classification:
- Validator Status:

---

# Bug Hunter B

## Agent Profile

...

## Findings

### Finding Template

#### <FINDING-ID> — <TITLE>

...

---

# Cross-Hunter Discussion

## Agreements

...

## Disagreements

...

## Challenges

...

## Open Questions

...

---

# Defense Rounds

## Round Template

### Round <N> — <FINDING-ID>

**Trigger:**

...

**Question / Objection:**

...

**Hunter Response:**

...

**Evidence Rechecked:**

...

**Hunter Position:**

- DEFEND
- NARROW
- RECLASSIFY
- RETRACT

**Main Agent Assessment:**

...

**Next Action:**

...

---

# Reclassifications

...

# Main Agent Review

...

# Current Status

...
```

The template is reusable.

Do not create a separate Bug Hunter file for each agent.

---

# 13. Bug Hunter Investigation Standard

A Bug Hunter MUST NOT merely search for suspicious code.

For every serious finding, it must answer:

> **Why is this actually a bug?**

The Bug Hunter MUST establish:

1. What behavior currently exists.
2. What behavior is expected.
3. Where the expected behavior comes from.
4. Why the difference matters.
5. Whether the difference is intentional.
6. Whether another legitimate interpretation exists.
7. Whether the behavior is environment-specific.
8. Whether the issue is caused by a requirement ambiguity.
9. Whether the issue is actually a design trade-off.
10. Whether sufficient evidence exists to call it a bug.

The Bug Hunter MUST NOT use:

> "This code looks wrong."

as sufficient reasoning.

---

# 14. Bug Hunter Reclassification Rule

Whenever a suspected bug could instead be:

* a valid design choice;
* an explicit limitation;
* a requirement ambiguity;
* an environmental dependency;
* an intentional trade-off;
* an unsupported thesis claim;
* an unproven assumption;

the Bug Hunter MUST explicitly test those alternatives.

A finding can be reclassified as:

```text
BUG
DESIGN CHOICE
LIMITATION
REQUIREMENT AMBIGUITY
THESIS CLAIM ISSUE
DOCUMENTATION ISSUE
ENVIRONMENT-SPECIFIC ISSUE
FALSE POSITIVE
UNRESOLVED
```

The goal is correctness of classification, not bug count.

---

# 15. Bug Hunter Expected-Behavior Rule

For every finding classified as a potential bug, the Bug Hunter MUST attempt to establish:

```text
CURRENT BEHAVIOR
        ↓
EXPECTED BEHAVIOR
        ↓
SOURCE OF EXPECTED BEHAVIOR
        ↓
BEHAVIORAL GAP
        ↓
CONSEQUENCE
```

The source of expected behavior may be:

* explicit specification;
* business rule;
* thesis claim;
* acceptance criterion;
* architecture invariant;
* documented contract;
* framework/platform contract;
* security requirement;
* domain rule;
* observable system invariant.

If no credible source for expected behavior exists, the finding MUST NOT be confidently classified as a bug.

---

# 16. Bug Hunter Self-Falsification

Before submitting a significant finding, each Bug Hunter MUST attempt to disprove their own conclusion.

Ask:

* What evidence would prove me wrong?
* Could this be intentional?
* Could another component guarantee the behavior?
* Could configuration explain the behavior?
* Could the thesis requirement be interpreted differently?
* Could the environment be responsible?
* Is the severity overstated?
* Is the observed behavior actually acceptable?

A strong finding survives its own attempted falsification.

---

# 17. Bug Hunter Peer Challenge

Bug Hunter A and B may communicate through the available agent communication mechanism.

They should use this for:

* independent verification;
* challenging weak reasoning;
* overlapping findings;
* evidence discovery;
* alternative interpretation;
* reproduction;
* cross-domain dependencies.

The second Bug Hunter should not simply confirm the first.

Example:

```text
[QUESTION]

Finding: F-003

I believe the behavior may be intentional rather than defective.

Please independently determine:
1. What the expected behavior actually is.
2. Where that expectation is established.
3. Whether the current implementation violates it.

Do not assume my conclusion.
```

---

# 18. Bug Hunter Completion Standard

A Bug Hunter is not considered complete merely because it says:

```text
done
```

An acceptable submission must include:

```text
STATUS: SUBMITTED

Findings:
...

Evidence:
...

Potential Non-Bugs / False Positives:
...

Open Questions:
...

Claims Requiring Examination:
...

Confidence:
...
```

The Main Agent must inspect and evaluate the submission.

---

# 19. Main Agent Bug Hunter Review Gate

After both Bug Hunters submit their initial investigations, the Main Agent MUST:

1. Read `02-bug-hunter.md`.
2. Compare both investigations.
3. Identify duplicates.
4. Identify contradictions.
5. Identify unsupported claims.
6. Verify the strongest claims against the codebase.
7. Check expected-behavior justification.
8. Reclassify weak findings where necessary.
9. Record unresolved questions.
10. Prepare the normalized finding set for the Validators.

The Main Agent MUST NOT automatically carry every Bug Hunter claim into validation.

---

# 20. Validator Workspace

`03-validator.md` must contain exactly three persistent Validator sections.

Recommended structure:

```md
# Validator Workspace

## Examination Status

- Overall Status:
- Current Round:
- Findings Under Examination:
- Open Objections:
- Evidence Requests:

---

# Validator A

## Examiner Profile

- Name:
- Perspective:
- Status:

## Examination

### Finding Examination Template

#### <FINDING-ID> — <TITLE>

**Initial Position:**

...

**Questions:**

- [ ] ...
- [ ] ...

**Evidence Requested:**

...

**Alternative Explanation:**

...

**Bug Hunter Response:**

...

**Counterargument:**

...

**Verdict:**

- CONFIRMED
- PARTIALLY CONFIRMED
- DISPUTED
- UNPROVEN
- REJECTED
- NEEDS EVIDENCE

**Remaining Objection:**

...

---

# Validator B

...

---

# Validator C

...

---

# Cross-Validator Discussion

## Agreement

...

## Disagreement

...

## Evidence Gaps

...

---

# Examination Rounds

## Round Template

### Round <N> — <FINDING-ID>

**Validator:**

...

**Challenge:**

...

**Evidence Requested:**

...

**Bug Hunter Response:**

...

**Validator Follow-Up:**

...

**Main Agent Assessment:**

...

**Current Verdict:**

...

**Next Action:**

...
```

All three Validators share this artifact.

---

# 21. Validator Mission

Validators are deliberately skeptical.

Their default position is:

> **The finding has not yet been proven.**

A Validator's responsibility is to test whether the Bug Hunter's reasoning survives examination.

Validators MUST ask difficult questions when justified.

They should test:

* technical correctness;
* expected behavior;
* evidence sufficiency;
* reproduction;
* alternative explanations;
* domain assumptions;
* business rules;
* implementation intent;
* architectural contracts;
* configuration;
* environment;
* edge conditions;
* severity;
* impact;
* thesis relevance;
* academic defensibility.

---

# 22. Validator Must Be Relentlessly Precise, Not Irrational

Validators should be "rewel" in the sense of being rigorous and persistent.

They MUST ask for missing proof.

They MUST challenge vague reasoning.

They MUST request concrete evidence.

They MUST continue an objection when a meaningful question remains unanswered.

However, Validators MUST NOT:

* invent objections;
* demand impossible proof;
* repeat the same objection without articulating what is missing;
* reject a finding merely because they dislike the implementation;
* confuse a design preference with a defect;
* force a bug classification when evidence does not support one.

The objective is rigorous examination, not obstruction.

---

# 23. Validator Question Set

For each significant finding, Validators should consider questions such as:

### Behavior

* What exactly happens?
* Under what conditions?
* Is the behavior reproducible?
* Is it deterministic?

### Expected Behavior

* Why should the system behave differently?
* Which requirement establishes the expected behavior?
* Is that requirement explicit or inferred?

### Intent

* Could this behavior be intentional?
* Is there another component that guarantees correctness?
* Is this a deliberate trade-off?

### Edge Cases

* What happens with empty input?
* Missing input?
* Invalid input?
* Maximum input?
* Minimum input?
* Repeated requests?
* Partial state?
* Concurrent execution?
* Failure during the middle of the operation?

### Error Handling

* What happens when the dependency fails?
* Is the error propagated correctly?
* Is the system left in an inconsistent state?
* Is retry safe?
* Is recovery possible?

### Data Integrity

* Can data become duplicated?
* Lost?
* Corrupted?
* Partially written?
* Incorrectly associated?

### Security

* Can the behavior cross an authorization boundary?
* Leak information?
* Bypass validation?
* Trust unverified state?

### Thesis Defense

* Does the claim in the thesis match the implementation?
* Is the evidence sufficient?
* Could an examiner reasonably challenge this conclusion?
* Is the methodology consistent with the claim?

---

# 24. Validator → Main Agent → Bug Hunter

Validators MUST NOT directly spawn or replace Bug Hunters.

The communication chain is:

```text
Validator
    ↓
Main Agent
    ↓
Existing Bug Hunter
    ↓
Main Agent
    ↓
Validator
```

The Main Agent acts as communication bridge and moderator.

Example:

```text
[EVIDENCE_REQUEST]

Finding: F-004

Validator: Validator B

Question:
Show why the behavior violates the business rule rather than merely
differing from the current implementation.

Required evidence:
<specific evidence>

Reason:
The current argument establishes a behavioral difference, but not yet
a defect.
```

The Main Agent forwards the request to the appropriate existing Bug Hunter.

---

# 25. Bug Hunter Defense Round

When a Validator challenges a finding, the responsible Bug Hunter MUST reassess the finding.

The Hunter must:

1. Re-open the relevant evidence.
2. Check the original reasoning.
3. Attempt self-falsification.
4. Answer the exact question.
5. Provide evidence.
6. Narrow the claim if necessary.
7. Reclassify the issue if necessary.
8. Retract the finding if it is wrong.
9. Defend the finding when evidence remains strong.

Use:

```text
DEFEND
NARROW
RECLASSIFY
RETRACT
```

The Hunter must not change its conclusion merely to satisfy the Validator.

---

# 26. Defense Round Template

Every meaningful challenge should be represented as a round.

Use:

```md
### Round <N> — <FINDING-ID>

**Trigger:**

Why the round started.

**Validator:**

Which examiner raised the issue.

**Objection / Question:**

Exact challenge.

**Evidence Requested:**

What must be proven.

**Bug Hunter Response:**

Evidence-based response.

**Self-Falsification Result:**

What the Hunter tried to disprove.

**Hunter Position:**

- DEFEND
- NARROW
- RECLASSIFY
- RETRACT

**Main Agent Assessment:**

...

**Validator Follow-Up:**

...

**Current Verdict:**

...

**Open Question:**

...

**Next Action:**

...
```

---

# 27. Why-Is-It-a-Bug Gate

This is a mandatory gate for every serious bug claim.

Before the finding can be considered a confirmed bug, the Main Agent must be able to answer:

```text
1. What is happening?
2. What should happen?
3. Why should it happen that way?
4. Where is that expectation established?
5. How does the implementation violate it?
6. What consequence follows?
7. Why is this not an intentional design decision?
8. Why is this not merely a limitation?
9. Why is this not simply missing specification?
10. What evidence proves the claim?
```

If the team cannot answer these questions, the finding remains unconfirmed.

---

# 28. Non-Bug Resolution

A successful investigation may determine:

> This is not a bug.

That outcome must be recorded explicitly.

Examples:

```text
FALSE POSITIVE
VALID DESIGN CHOICE
ACCEPTED LIMITATION
REQUIREMENT AMBIGUITY
THESIS OVERCLAIM
ENVIRONMENTAL CONDITION
DOCUMENTATION ISSUE
UNRESOLVED
```

The Main Agent must explain why the original suspicion did not qualify as a bug.

Do not hide rejected findings.

Rejected findings are valuable because they demonstrate that the review was adversarial rather than biased toward finding defects.

---

# 29. Finding Lifecycle

A finding SHOULD move through:

```text
POTENTIAL
    ↓
INVESTIGATING
    ↓
EVIDENCE_ESTABLISHED
    ↓
UNDER_EXAMINATION
    ↓
DEFENDED / CHALLENGED
    ↓
ADJUDICATED
```

Final classifications:

```text
CONFIRMED BUG
PARTIALLY CONFIRMED
DISPUTED
UNPROVEN
REJECTED
NON-BUG
UNRESOLVED
```

Do not use `CONFIRMED BUG` prematurely.

---

# 30. Severity

Use:

```text
CRITICAL
HIGH
MEDIUM
LOW
INFORMATIONAL
```

Severity must reflect actual impact and evidence.

Do not classify a finding as critical merely because it concerns an important module.

Explain severity through consequences.

---

# 31. Evidence Standard

Prefer evidence in roughly this order:

1. Direct reproducible behavior
2. Executed test or controlled reproduction
3. Source-code evidence
4. Runtime/log evidence
5. Database/state evidence
6. Configuration evidence
7. Explicit project/thesis requirement
8. Official technical documentation
9. Reasoned inference
10. Speculation

Speculation MUST NOT be represented as proof.

---

# 32. Claim / Evidence Separation

Every important conclusion must distinguish:

```text
CLAIM
EVIDENCE
INFERENCE
ASSUMPTION
VERIFIED FACT
UNRESOLVED QUESTION
```

The Main Agent should ensure that these categories are not mixed.

Example:

```text
Claim:
Refresh token rotation is missing.

Evidence:
Function X returns the same token family without invalidation.

Inference:
The implementation therefore permits reuse.

Assumption:
The intended security model requires rotation.

Required verification:
Confirm the intended security requirement.
```

This is preferable to presenting the entire paragraph as fact.

---

# 33. No-Context Deep Tracking

When user context is absent, each Bug Hunter should systematically trace the major logic paths rather than stopping after finding obvious issues.

For each important feature, consider:

```text
Input
 ↓
Validation
 ↓
Normalization
 ↓
Business Rules
 ↓
State Transition
 ↓
Persistence
 ↓
External Interaction
 ↓
Response
 ↓
Error / Recovery
```

Also examine:

```text
Happy path
Failure path
Boundary path
Unexpected input path
Repeated execution
Concurrent execution
Partial failure
Recovery path
Retry path
Cancellation path
```

Follow the actual project architecture and expand this model where relevant.

---

# 34. Main Agent Continuous Review

The Main Agent must not merely wait for reports.

At every major stage it should independently ask:

```text
What has been proven?
What has not been proven?
What is merely suspected?
What may be a false positive?
What evidence is missing?
What assumption is carrying the argument?
What has changed since the last review?
```

The Main Agent should update the corresponding artifact whenever these states change.

---

# 35. Artifact Editing Discipline

The Main Agent is the primary editor of the four persistent artifacts.

Subagents SHOULD return findings, questions, evidence, and verdicts to the Main Agent rather than independently restructuring the shared artifacts.

This prevents conflicting edits and preserves document integrity.

The Main Agent should:

1. Read the current artifact.
2. Apply the update.
3. Re-read relevant sections.
4. Verify that previous evidence was not accidentally lost.
5. Continue orchestration.

Never overwrite a shared artifact based solely on stale context.

---

# 36. Cross-Agent Consistency

The Main Agent must continuously compare:

```text
01-initial-thesis.md
        ↕
02-bug-hunter.md
        ↕
03-validator.md
        ↕
04-final-report.md
```

Important contradictions must be resolved or explicitly recorded.

The final report must not contradict the final adjudicated state of the Bug Hunter and Validator workspaces.

---

# 37. Examination Rounds

A round ends only when its question is adequately resolved.

A valid round may result in:

```text
RESOLVED
CONFIRMED
NARROWED
RECLASSIFIED
RETRACTED
REJECTED
NEEDS_MORE_EVIDENCE
```

Do not close a round simply because an agent submitted a response.

The response must address the actual objection.

---

# 38. Anti-Loop Rule

Do not allow endless repetition.

When the same objection is repeated:

1. Identify the exact unresolved point.
2. Ask the Validator to specify the missing evidence.
3. Ask the Bug Hunter to address that evidence.
4. If the evidence has been provided, require a verdict.
5. If the Validator cannot articulate a new substantive objection, close the loop.

Repeated disagreement without a new evidentiary issue must not continue indefinitely.

---

# 39. Main Agent Final Adjudication

Before producing the final report, the Main Agent MUST inspect:

```text
02-bug-hunter.md
03-validator.md
```

For each finding, determine:

```text
Current Classification
Evidence Quality
Expected-Behavior Justification
Validator Verdicts
Unresolved Objections
Bug Hunter Defense
Main Agent Decision
```

The Main Agent must not simply choose the majority opinion.

Evidence matters more than voting.

---

# 40. Final Report Structure

`04-final-report.md` SHOULD use:

```md
# Final Thesis Bug Hunter Report

## 1. Executive Verdict

...

## 2. Investigation Scope

...

## 3. Method

...

## 4. Confirmed Bugs

### <FINDING-ID> — <TITLE>

- Classification:
- Severity:
- Location:
- Why It Is a Bug:
- Expected Behavior:
- Actual Behavior:
- Evidence:
- Impact:
- Thesis Impact:
- Validator Assessment:
- Final Reasoning:

---

## 5. Partially Confirmed Findings

...

---

## 6. Disputed Findings

...

---

## 7. Rejected / Non-Bug Findings

### <FINDING-ID>

- Original Claim:
- Why It Looked Like A Bug:
- Why It Is Not A Bug:
- Correct Classification:
- Evidence:

---

## 8. Unproven / Unresolved Findings

...

---

## 9. Strongest Validator Objections

...

---

## 10. Strongest Bug Hunter Defenses

...

---

## 11. Recommendations

### Technical Recommendations

...

### Business / Behavioral Recommendations

...

### Thesis Recommendations

...

### Evidence / Defense Recommendations

...

---

## 12. Remaining Risks

...

## 13. Defense Readiness

...

## 14. Final Conclusion

...

```

---

# 41. Recommendations Must Be Evidence-Based

Recommendations must follow confirmed or strongly supported findings.

Do not turn every rejected finding into a recommendation.

A recommendation should state:

* What should change.
* Why.
* Which finding supports it.
* Expected benefit.
* Important trade-offs.
* Priority.

---

# 42. Final Report Agreement Standard

The final report should represent the final adjudicated position.

"Agreement" does not mean every agent must agree emotionally or intellectually.

It means:

* objections were heard;
* evidence was considered;
* counterarguments were addressed;
* classifications were justified;
* unresolved uncertainty is explicitly recorded.

A finding can remain `DISPUTED` when the evidence genuinely supports competing interpretations.

Do not manufacture consensus.

---

# 43. Completion Gate

The execution may be considered complete when:

```text
[ ] Initial thesis/context is finalized
[ ] Bug Hunter A submitted
[ ] Bug Hunter B submitted
[ ] Bug Hunter findings were reviewed
[ ] Validator A examined relevant findings
[ ] Validator B examined relevant findings
[ ] Validator C examined relevant findings
[ ] Required evidence requests were addressed
[ ] Significant objections were resolved or explicitly classified
[ ] Findings were adjudicated
[ ] Recommendations were derived from evidence
[ ] Final report reflects the final state
```

The five persistent agents must remain the same throughout the workflow.

---

# 44. Completion Does Not Mean "No Bugs"

A successful execution may conclude with:

```text
No confirmed bugs were established.
```

That is valid when supported by evidence.

The skill must optimize for:

> **Correct classification**

rather than:

> **Maximum bug count**

---

# 45. Context Recovery

If the Main Agent experiences context compression, uncertainty, interruption, or partial memory loss, it MUST use the persistent artifacts to recover.

Recovery sequence:

```text
STOP
 ↓
Read 01-initial-thesis.md
 ↓
Read 02-bug-hunter.md
 ↓
Read 03-validator.md
 ↓
Read 04-final-report.md when needed
 ↓
Identify current investigation state
 ↓
Identify active round
 ↓
Identify unresolved findings
 ↓
Identify pending evidence requests
 ↓
Identify next action
 ↓
Resume orchestration
```

Do not reconstruct the state from memory if the artifacts contain the state.

---

# 46. Agent Communication Labels

Use clear communication categories:

```text
[DISCOVERY]
[QUESTION]
[EVIDENCE_REQUEST]
[CHALLENGE]
[DEFENSE]
[COUNTERARGUMENT]
[CONCESSION]
[RETRACTION]
[RECLASSIFICATION]
[VERDICT]
[STATUS]
[BLOCKED]
[ESCALATION]
```

This makes long-running discussions easier to recover after context compression.

---

# 47. Persistent Round Tracking

Every meaningful examination cycle should identify:

```text
Finding ID
Round Number
Initiating Agent
Question
Evidence Requested
Response
Follow-Up
Current State
```

Do not rely on conversational memory to determine how many times a finding has been challenged.

---

# 48. Orchestrator Operating Loop

Use this workflow:

```text
1. Read user request
2. Determine available project/thesis context
3. Determine execution scope
4. Create execution folder under ./docs/superskill/
5. Create exactly four persistent artifacts
6. Build 01-initial-thesis.md
7. Read it
8. Critique it
9. Correct it
10. Repeat until sufficiently complete
11. Initialize the Bug Hunter and Validator templates
12. Spawn exactly 2 persistent Bug Hunters
13. Provide the finalized initial thesis context
14. Allow independent investigation
15. Collect Bug Hunter findings
16. Review and normalize findings
17. Spawn exactly 3 persistent Validators
18. Provide the same initial thesis context and normalized findings
19. Run adversarial examination
20. Route follow-up questions through the Main Agent
21. Continue defense rounds
22. Require explicit bug-vs-non-bug reasoning
23. Resolve or classify objections
24. Perform final adjudication
25. Write the final report
26. Review the final report
27. Correct any inconsistency
28. Verify the four artifacts remain coherent
29. Conclude the execution
```

---

# 49. Fundamental Mental Model

The system consists of three roles:

```text
BUG HUNTERS
"Something may be wrong. Let us prove whether it actually is."

VALIDATORS
"Prove it. Show me why it is a bug, not merely a difference."

MAIN AGENT
"Both claims are hypotheses until supported by evidence."
```

The Main Agent is responsible for maintaining intellectual discipline between both sides.

---

# 50. Fundamental Rule

The core rule of this skill is:

> **Never call something a bug merely because its behavior differs from what someone expected. Establish the expected behavior, establish the actual behavior, prove the difference, test alternative explanations, and make the claim survive adversarial examination.**

And the operational rule is:

> **Persist the context. Persist the findings. Persist the examination. Persist the final decision. Use exactly four artifacts so the investigation remains recoverable, structured, and resistant to context loss.**

The Main Agent orchestrates.

Two Bug Hunters investigate and defend.

Three Validators challenge and verify.

The four artifacts preserve the investigation state.
