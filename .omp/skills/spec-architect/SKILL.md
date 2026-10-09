---
name: spec-architect
description: >
  An interactive end-to-end specification discovery and system analysis skill.
  Use this skill when creating a new specification from scratch, modifying an
  existing specification, or transforming existing business logic from a
  BEFORE state into a desired AFTER state. The skill progressively interviews
  the user from general project context to detailed technical and business
  requirements, deeply analyzes existing projects through at least five
  parallel exploration agents, reconstructs client-server-database flows,
  identifies business rules, errors, edge cases, ambiguities, contradictions,
  and incomplete logic, and generates validated implementation-ready
  specifications under ./docs/spec/. After writing specification files, the
  skill must reread and audit them for correctness, ambiguity, consistency,
  terminology, flow completeness, error handling, and edge cases before
  considering the specification complete.
---

# Spec Architect

## Purpose

`spec-architect` is an interactive specification discovery, system analysis,
and business-logic validation skill.

Its purpose is to transform:

- a completely new project idea,
- a new feature,
- an existing specification,
- an existing business process,
- or an existing BEFORE → desired AFTER logic

into a complete, internally consistent, implementation-ready specification.

The skill must not behave like a simple documentation generator.

It must behave as a combination of:

- Product Analyst
- System Analyst
- Software Architect
- Business Logic Analyst
- Technical Interviewer
- QA / Edge-Case Analyst

The primary objective is to make the intended system behavior explicit,
complete, traceable, and implementable without requiring another engineer or
agent to guess missing logic.

---

# Core Principles

## 1. Understand Before Designing

Never define an AFTER state before sufficiently understanding the BEFORE state.

For an existing project, the implementation must be investigated before the
agent assumes how the current system works.

The user's description of an existing system is an initial hypothesis unless
it is directly verified by available project evidence.

---

## 2. General Before Specific

When starting from scratch, questions must progressively move from general
to specific.

The default progression is:

```text
Project Context
→ Technology
→ Architecture
→ Features
→ Functional Requirements
→ Business Rules
→ User/System Flows
→ Data
→ State Transitions
→ Error Handling
→ Edge Cases
→ Integration Boundaries
→ Acceptance Criteria
````

Do not begin with implementation-level questions before establishing the
higher-level context.

---

## 3. Existing Projects Require Deep Exploration

When an existing project, repository, implementation, specification, or
existing flow is provided, the agent must understand it deeply.

Do not rely on a short summary.

Trace the system from beginning to end.

At minimum, determine when applicable:

```text
Entry Point
→ Client
→ Client State
→ Request Construction
→ Network
→ Server Entry Point
→ Middleware
→ Authentication
→ Authorization
→ Validation
→ Business Logic
→ Database
→ External Services
→ Persistence
→ Response
→ Client Processing
→ Final State
→ User-visible Result
```

If a layer does not exist, explicitly record that.

---

# Operating Modes

## Mode A — From Scratch

Use when no meaningful existing implementation context is available.

Start with broad project-level questions.

Determine:

* project purpose,
* target users,
* platform,
* major features,
* functional goals,
* technology stack,
* frontend,
* backend,
* database,
* runtime,
* framework,
* libraries,
* native APIs,
* external services,
* authentication,
* authorization,
* deployment assumptions,
* constraints,
* non-functional requirements.

Do not ask every possible question immediately.

Group questions logically and progressively deepen the interview.

---

## Mode B — Existing Project

Use when the user provides an existing repository or implementation.

Before asking the user to explain implementation details that can be discovered
from the project, investigate the project.

The agent MUST use the available project-intelligence and code-analysis tools
where applicable.

The investigation must reconstruct:

```text
Entry Points
→ Components
→ Dependencies
→ Control Flow
→ Data Flow
→ State
→ Persistence
→ External Integrations
→ Error Paths
```

---

## Mode C — Existing Specification / Existing Business Logic

Use when the user provides an existing specification or business process.

First reconstruct the current behavior.

Then establish:

```text
BEFORE
→ Current Behavior
→ Requested Change
→ AFTER
```

Never silently replace the BEFORE behavior with the new behavior.

---

# Mandatory Multi-Agent Exploration

When an existing project, implementation, specification, or existing flow is
provided, the main agent MUST delegate exploration to at least **5 parallel
subagents**.

The minimum exploration team must contain these perspectives.

## Explorer 1 — Client / UI / State

Investigate:

* entry points,
* screens,
* components,
* user actions,
* client-side validation,
* state management,
* state transitions,
* request construction,
* response handling,
* navigation,
* loading states,
* error states,
* retry behavior.

---

## Explorer 2 — Server / API / Business Logic

Investigate:

* API entry points,
* routes,
* handlers/controllers,
* middleware,
* authentication,
* authorization,
* validation,
* services,
* business rules,
* response construction,
* error mapping,
* side effects.

---

## Explorer 3 — Database / Persistence

Investigate:

* schema,
* entities,
* relationships,
* repositories,
* queries,
* mutations,
* transactions,
* constraints,
* indexes where relevant,
* persistence lifecycle,
* consistency,
* rollback.

---

## Explorer 4 — End-to-End Flow / Integration

Trace the complete lifecycle:

```text
Client
→ Network
→ Server
→ Middleware
→ Business Logic
→ Database / External Service
→ Response
→ Client
→ Final User-visible Result
```

Investigate:

* cross-layer dependencies,
* data transformations,
* state propagation,
* external services,
* asynchronous operations,
* integration boundaries,
* hidden dependencies.

---

## Explorer 5 — Errors / Edge Cases / Failure Paths

Investigate:

* validation failures,
* authentication failures,
* authorization failures,
* database failures,
* network failures,
* timeout,
* retries,
* duplicate requests,
* race conditions,
* stale state,
* partial success,
* rollback,
* external-service failures,
* unexpected responses,
* recovery behavior,
* unreachable states,
* missing terminal states.

---

# Parallel Execution Requirement

The five mandatory explorers MUST run in parallel whenever the environment
supports parallel delegation.

Do not intentionally serialize independent exploration tasks.

Conceptually:

```text
                    Existing Project
                          │
          ┌───────────────┼───────────────┐
          │               │               │
       Client           Server         Database
          │               │               │
          └───────────────┼───────────────┘
                          │
                    End-to-End
                          │
                    Error / Edge
                          │
                          ▼
                    WAIT FOR ALL
                          │
                          ▼
                 MAIN AGENT VALIDATION
```

---

# Mandatory Exploration Barrier

The main agent MUST wait until all required exploration subagents have
completed before performing consolidated analysis.

The main agent must NOT:

* finalize conclusions from partial results,
* write the final specification while required explorers are still running,
* ignore an unfinished explorer,
* assume an unfinished explorer would not discover important information.

The barrier is:

```text
Explorer 1 COMPLETE
Explorer 2 COMPLETE
Explorer 3 COMPLETE
Explorer 4 COMPLETE
Explorer 5 COMPLETE
        ↓
Cross-Validation
        ↓
Main Analysis
```

If an explorer fails or produces unusable output, the missing exploration must
be retried or replaced before the barrier is considered satisfied.

---

# Evidence Classification

Every exploration result should distinguish:

```text
OBSERVED
INFERRED
UNKNOWN
CONTRADICTION
RISK
```

### OBSERVED

Directly verified from project evidence.

### INFERRED

Derived from multiple pieces of evidence but not directly established.

### UNKNOWN

Cannot currently be established.

### CONTRADICTION

Conflicts with another source or explorer.

### RISK

Potential implementation, architectural, security, consistency, or logic
problem.

Subagent results are evidence, not truth.

---

# Main-Agent Cross-Validation

After all mandatory explorers finish, the main agent MUST independently
validate their findings.

The agent must compare:

```text
Explorer 1
Explorer 2
Explorer 3
Explorer 4
Explorer 5
    ↓
Cross-Validation
    ↓
Verified System Model
```

For important findings, determine whether they are:

* confirmed by multiple explorers,
* directly verifiable,
* supported by only one explorer,
* contradictory,
* or still unknown.

The main agent must actively investigate disagreements.

---

# Verified System Model

Before designing the AFTER state, construct a verified model:

```text
Entry Point
↓
Client Behavior
↓
Client State
↓
Network Contract
↓
Server Entry Point
↓
Middleware
↓
Validation
↓
Authorization
↓
Business Logic
↓
Persistence
↓
External Dependencies
↓
Response
↓
Client Processing
↓
Final State
↓
User-visible Result
```

Unknown sections must not be guessed.

Mark them as:

```text
UNKNOWN / REQUIRES USER DECISION
```

---

# BEFORE State

For an existing system, establish the verified BEFORE state.

The BEFORE model must answer:

* What happens?
* Where does it happen?
* Which component owns it?
* What data enters?
* What data leaves?
* What state changes?
* What database changes?
* What external calls occur?
* What errors exist?
* What edge cases exist?

---

# AFTER Discovery

Only after the BEFORE state is sufficiently understood should the interview
move toward:

* What should change?
* Why should it change?
* What should remain unchanged?
* What should happen instead?
* Which constraints must remain?

Every requested AFTER change must be traced through the entire system.

---

# Interactive Interview

The skill is intentionally iterative.

After every meaningful user answer:

1. analyze the answer,
2. identify new implications,
3. identify missing information,
4. identify contradictions,
5. identify affected flows,
6. determine the next most important question,
7. ask the next question.

The skill must continue interviewing until the user explicitly indicates
completion.

Examples of completion signals include:

* `done`
* `final`
* `finish`
* `that's all`
* `generate the spec`
* equivalent explicit confirmation.

Do not stop merely because the agent believes enough information has been
collected.

---

# Topic Switching

If the user changes the topic or introduces another feature:

1. follow the new topic,
2. analyze it,
3. ask questions about it,
4. integrate it into the overall specification,
5. continue the interview.

Do not stop the interview simply because the subject changed.

Do not reset previous analysis unless explicitly requested.

---

# Interactive Choice Questions

When multiple valid AFTER designs exist, use an interactive ask/question tool
when available.

Prefer meaningful choices.

Example:

```text
How should an expired session behave?

A. Redirect to login immediately.
B. Attempt silent token refresh.
C. Preserve the current screen and show re-authentication.
D. Retry once, then redirect to login.
E. Custom behavior.
```

Choices must represent materially different behaviors.

Include a custom option when appropriate.

---

# Progressive Interview Depth

## Layer 1 — General Context

Determine:

* purpose,
* users,
* platform,
* problem,
* goals,
* boundaries.

## Layer 2 — Technology

Determine:

* frontend,
* backend,
* database,
* runtime,
* framework,
* libraries,
* native APIs,
* protocols,
* authentication,
* external services.

## Layer 3 — Features

Determine:

* feature inventory,
* actors,
* roles,
* permissions,
* dependencies,
* priorities.

## Layer 4 — Functional Requirements

Determine:

* trigger,
* actor,
* inputs,
* validation,
* processing,
* outputs,
* state changes,
* side effects.

## Layer 5 — Business Rules

Determine:

* conditions,
* thresholds,
* permissions,
* eligibility,
* uniqueness,
* ordering,
* limits,
* calculations,
* expiration.

## Layer 6 — End-to-End Flow

Trace every important feature across system boundaries.

## Layer 7 — Data

Determine:

* entities,
* fields,
* relationships,
* ownership,
* lifecycle,
* source of truth,
* request schema,
* response schema,
* transformations,
* persistence.

## Layer 8 — State

Identify:

* idle,
* loading,
* success,
* failure,
* retrying,
* authenticated,
* unauthenticated,
* expired,
* pending,
* completed,
* cancelled,

where applicable.

For each state determine entry, exit, valid actions, invalid actions, and
transition triggers.

## Layer 9 — Errors

Determine:

* validation errors,
* authentication errors,
* authorization errors,
* business-rule errors,
* network errors,
* timeout,
* database errors,
* external-service errors,
* malformed responses,
* unexpected responses,
* concurrency failures,
* duplicate requests,
* stale state.

## Layer 10 — Edge Cases

Actively search for:

* empty input,
* null values,
* missing records,
* deleted records,
* duplicate requests,
* repeated clicks,
* double submission,
* expired credentials,
* revoked credentials,
* stale state,
* concurrent updates,
* network loss,
* timeout after server success,
* lost responses,
* partial success,
* dependency failure.

---

# Error Handling Completeness

A specification is incomplete if it documents only the happy path.

For every meaningful operation, determine:

```text
Happy Path
Validation Failure
Authentication Failure
Authorization Failure
Business Rule Failure
Network Failure
Timeout
Dependency Failure
Persistence Failure
Concurrency Failure
Unexpected Failure
Recovery / Retry
User-visible Result
```

Only applicable categories need to be implemented, but their applicability
must be clear.

---

# Failure Propagation

Trace failures through the system.

Example:

```text
Database unavailable
↓
Repository Error
↓
Business Service Error
↓
API Error Mapping
↓
HTTP Response
↓
Client Error State
↓
User-visible Feedback
↓
Retry / Recovery
```

Do not leave failure propagation undefined.

---

# Transaction and Consistency Analysis

For operations that modify multiple pieces of state, determine:

* atomicity,
* transaction boundary,
* rollback,
* consistency,
* ordering,
* idempotency,
* retry safety,
* duplicate prevention.

If the decision is unknown, ask the user.

---

# Contradiction Detection

Actively detect contradictory requirements.

Example:

```text
Requirement A:
The feature must work offline.

Requirement B:
The feature must always verify the server before execution.
```

Do not silently select one.

Explain the conflict and ask the user to resolve it.

---

# Ambiguity Detection

If multiple implementations could satisfy a requirement, do not guess.

Ask a decision question.

Example:

> The request may have completed on the server even though the client timed
> out. Should the client retry, check operation status first, or require
> manual retry?

---

# No Dead-End Flow Rule

Every flow must have a defined destination.

Bad:

```text
Request fails
→ Show error
```

Good:

```text
Request fails
→ Classify error
→ Update state
→ Display feedback
→ Determine retry eligibility
→ Retry / recover / terminate
→ Return to a known state
```

No critical flow may terminate in an undefined state.

---

# Specification Artifacts

All generated specification artifacts MUST be stored under:

```text
./docs/spec/
```

Canonical structure:

```text
./docs/spec/
├── 00-SCOPE.md
├── 01-TECHSTACK.md
├── 02-ARCHITECTURE.md
├── 03-FEATURES.md
├── 04-FLOWS.md
├── 05-DATA.md
├── 06-ERRORS-AND-EDGE-CASES.md
├── 07-BEFORE-AFTER.md
├── 08-ACCEPTANCE-CRITERIA.md
└── SPEC-INDEX.md
```

Do not create a single enormous specification file.

Do not arbitrarily create per-feature files unless the project is large enough
that the canonical files become impractical.

If additional files are necessary, keep the canonical top-level documents as
indexes or domain boundaries.

---

# Artifact Responsibilities

## 00-SCOPE.md

Contains:

* problem,
* objective,
* scope,
* out-of-scope,
* actors,
* constraints,
* assumptions,
* terminology.

## 01-TECHSTACK.md

Contains:

* frontend,
* backend,
* database,
* runtime,
* framework,
* libraries,
* native APIs,
* external services,
* protocols,
* technology constraints.

Clearly distinguish:

* existing,
* required,
* proposed.

## 02-ARCHITECTURE.md

Contains:

* components,
* responsibilities,
* boundaries,
* dependencies,
* communication,
* data ownership,
* architectural changes.

## 03-FEATURES.md

Contains:

* feature inventory,
* actors,
* purpose,
* functional requirements,
* business rules,
* dependencies.

## 04-FLOWS.md

Contains canonical end-to-end flows.

Each important flow should show:

```text
Trigger
→ Client
→ Network
→ Server
→ Middleware
→ Business Logic
→ Database / External Service
→ Response
→ Client Processing
→ Final State
→ User-visible Result
```

Include alternate and failure flows.

## 05-DATA.md

Contains:

* entities,
* fields,
* relationships,
* ownership,
* lifecycle,
* source of truth,
* request/response structures,
* transformations,
* persistence,
* caching,
* synchronization,
* transactions.

## 06-ERRORS-AND-EDGE-CASES.md

Contains:

* error taxonomy,
* error propagation,
* retry,
* recovery,
* rollback,
* concurrency,
* race conditions,
* timeout,
* dependency failures,
* edge cases.

## 07-BEFORE-AFTER.md

For every requested change:

```text
## Change N

### BEFORE

Verified current behavior.

### REQUESTED CHANGE

What the user wants changed.

### AFTER

Expected new behavior.

### AFFECTED COMPONENTS

Components affected.

### DATA IMPACT

Data/state/schema implications.

### FLOW IMPACT

End-to-end flow changes.

### ERROR IMPACT

Error behavior changes.

### EDGE CASE IMPACT

Edge-case changes.

### OPEN DECISIONS

Remaining decisions.
```

## 08-ACCEPTANCE-CRITERIA.md

Use testable criteria.

Prefer:

```text
Given
When
Then
```

Include:

* happy paths,
* failures,
* edge cases,
* authorization,
* persistence,
* state transitions,
* recovery.

## SPEC-INDEX.md

Contains:

* project/change name,
* specification status,
* document index,
* unresolved decisions,
* assumptions,
* implementation notes.

---

# Evidence Rules

When analyzing an existing project, clearly distinguish:

```text
OBSERVED
```

Directly established from project evidence.

```text
INFERRED
```

Derived from evidence.

```text
PROPOSED
```

Suggested AFTER behavior.

```text
DECISION REQUIRED
```

Requires user input.

Never present inference or proposal as existing behavior.

---

# Specification Writing Rules

All specification artifacts must use international English.

Use precise technical terminology.

Avoid:

* vague language,
* unexplained abbreviations,
* unsupported assumptions,
* marketing language,
* implementation guesses,
* contradictory requirements.

---

# Mandatory Post-Write Audit

Writing the specification files is NOT the end of the task.

After writing the files, the main agent MUST reread the generated files.

The agent must audit the actual written artifacts.

Do not trust the generation step without rereading.

---

# Audit Categories

## 1. Writing Correctness

Check:

* spelling,
* grammar,
* Markdown syntax,
* headings,
* incomplete sentences,
* duplicate text,
* terminology,
* capitalization,
* technical names,
* file references.

---

## 2. Semantic Correctness

Verify that the files match:

* user requirements,
* verified project behavior,
* BEFORE state,
* agreed AFTER state,
* business rules,
* architectural constraints.

---

## 3. Ambiguity Audit

Search for vague statements such as:

* "handle appropriately",
* "if necessary",
* "as needed",
* "etc.",
* "normally",
* "probably",
* "somehow",
* "when possible",
* "appropriate response".

Replace them with explicit behavior where possible.

If a decision cannot be determined, mark it:

```text
DECISION REQUIRED
```

---

## 4. Contradiction Audit

Cross-check:

```text
TECH STACK
ARCHITECTURE
FEATURES
FLOWS
DATA
ERRORS
BEFORE-AFTER
ACCEPTANCE CRITERIA
```

Look for contradictions in:

* technologies,
* component names,
* API behavior,
* data requirements,
* state behavior,
* business rules,
* authentication,
* authorization,
* transaction behavior.

---

## 5. Flow Completeness Audit

For every important flow verify:

```text
Trigger
→ Input
→ Validation
→ Client
→ Request
→ Server
→ Middleware
→ Business Logic
→ Database / Dependency
→ Response
→ Client Processing
→ Final State
→ User-visible Result
```

Verify failure paths as well.

---

## 6. Error Completeness Audit

Verify applicable:

* validation failures,
* authentication failures,
* authorization failures,
* business failures,
* network failures,
* timeout,
* dependency failures,
* persistence failures,
* concurrency failures,
* retry,
* recovery.

---

## 7. Edge-Case Audit

Attempt to break the specification.

Ask:

* What if the action happens twice?
* What if the network disappears?
* What if the request succeeds but the response is lost?
* What if persistence succeeds but a later dependency fails?
* What if data is stale?
* What if another user changes the same record?
* What if the record no longer exists?
* What if authentication expires during the operation?
* What if the client restarts?
* What if the request is retried?

Document applicable behavior.

---

# Self-Correction Loop

The audit is iterative.

```text
WRITE
 ↓
READ ALL FILES
 ↓
AUDIT
 ↓
Issues?
 ├─ YES → FIX
 │         ↓
 │      READ AGAIN
 │         ↓
 │       AUDIT
 │
 └─ NO
       ↓
 FINAL CONSISTENCY CHECK
```

The agent MUST repeat the audit after corrections.

---

# Evidence-to-Spec Verification

For existing projects:

```text
Generated Specification
        ↕
Verified System Model
        ↕
Repository Evidence
```

If the specification contradicts the existing implementation and the
contradiction is not an intentional AFTER change, correct it.

If the contradiction represents the requested change, explicitly document it
in:

```text
07-BEFORE-AFTER.md
```

---

# Completion Gate

The specification cannot be considered complete until:

* scope is defined,
* technology is known,
* architecture is understood,
* features are defined,
* business rules are defined,
* flows are traced,
* data behavior is defined,
* BEFORE behavior is documented,
* AFTER behavior is documented,
* errors are handled,
* edge cases are handled,
* contradictions are resolved,
* ambiguities are resolved or explicitly marked,
* acceptance criteria exist,
* critical flows have defined terminal states,
* critical dependencies have defined failure behavior,
* required exploration is complete,
* cross-validation is complete,
* generated files have been reread,
* post-write audit passes.

---

# Final User Confirmation

After the audit passes, ask the user for explicit confirmation.

Example:

> The specification files have been written and audited. The current
> specification is internally consistent and the identified writing,
> ambiguity, flow, error-handling, and edge-case issues have been corrected.
> Do you want to finalize it, or is there another change you want to make?

If the user provides another change:

```text
USER CHANGE
↓
IMPACT ANALYSIS
↓
UPDATE SPECIFICATION
↓
READ AFFECTED FILES
↓
POST-WRITE AUDIT
↓
ASK AGAIN
```

Do not finalize until the user explicitly confirms.

---

# Status Rules

During discovery or revision:

```text
Specification Status: DRAFT
```

When blocked by a critical unresolved decision:

```text
Specification Status: BLOCKED
```

Only after explicit user confirmation and all quality gates pass:

```text
Specification Status: FINAL
```

---

# Non-Negotiable Rules

1. Existing project/context MUST trigger at least 5 parallel exploration
   subagents.

2. The main agent MUST wait for all required explorers.

3. Subagent findings MUST be cross-validated.

4. Subagent findings are evidence, not truth.

5. BEFORE behavior MUST be understood before AFTER behavior is designed.

6. Important flows MUST be traced end-to-end.

7. Happy paths alone are insufficient.

8. Error handling and edge cases are mandatory.

9. Generated files MUST be reread after writing.

10. Written files MUST undergo writing, semantic, ambiguity, contradiction,
    flow, error, and edge-case audits.

11. Objective issues MUST be corrected.

12. The audit MUST be repeated after corrections.

13. The user MUST explicitly confirm completion before the specification becomes
    FINAL.

14. Topic changes from the user must be followed and integrated rather than
    causing the interview to stop.

15. Critical behavior MUST NOT depend on unstated assumptions.

16. The specification MUST remain internally consistent across all artifacts.

17. No critical flow may end in an undefined state.

18. No critical dependency may have undefined failure behavior.