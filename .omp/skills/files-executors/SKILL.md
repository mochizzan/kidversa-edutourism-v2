---

name: files-executors
description: Execute actionable instructions from arbitrary project-related documents through a disciplined, persistent, multi-agent orchestration workflow. Dynamically interpret plans, specifications, recommendations, audits, requirements, reports, and other informative files; create and continuously maintain a persistent task state; delegate exploration and implementation to appropriately scoped subagents; verify all delegated work; and use the task file as external memory for reliable recovery from context loss or compression.
-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

# File Executor

## Purpose

`file-executor` is a persistent, disciplined, multi-agent orchestration skill for executing actionable information contained in arbitrary project-related documents.

The input document may be a:

* Plan
* Specification
* Recommendation
* Audit
* Technical design
* Requirements document
* TODO list
* Architecture document
* Review report
* Migration plan
* Implementation notes
* Bug report
* Or any other project-related document containing information that may require action within the codebase

The skill MUST NOT depend on a specific filename, document structure, or document type.

Its purpose is to transform relevant information from the input document into verified, controlled, and traceable codebase changes.

The Main Agent acts as the **Orchestrator**.

Subagents perform delegated exploration, analysis, implementation, and validation work.

The Orchestrator owns the overall execution state and is responsible for determining whether delegated work is actually complete and correct.

---

# 1. Core Operating Principles

## 1.1 The Main Agent Is the Orchestrator

The Main Agent MUST operate primarily as an orchestrator rather than as a worker.

The Orchestrator is responsible for:

* Understanding the source document
* Creating the persistent task file
* Reviewing and correcting the task file
* Decomposing the work
* Assigning sections to subagents
* Coordinating parallel work
* Reviewing subagent results
* Resolving conflicts
* Planning implementation
* Delegating implementation
* Reviewing implementation
* Validating results
* Maintaining persistent state
* Recovering from context loss
* Determining final completion

The Orchestrator SHOULD delegate work whenever meaningful delegation is possible.

The Orchestrator MUST NOT unnecessarily duplicate work already assigned to subagents.

---

## 1.2 Persistent State Is Mandatory

Conversational context MUST NOT be treated as the sole source of execution state.

Every execution MUST have a persistent task file.

The task file acts as:

* External memory
* Execution state
* Task decomposition
* Delegation registry
* Progress tracker
* Validation tracker
* Dependency map
* Recovery mechanism
* Execution history
* Source of truth

Important decisions and progress MUST be reflected in the task file.

---

# 2. Persistent Task File

All task files MUST be stored under:

```text
./docs/superskill/task/
```

The filename MUST follow:

```text
YYYY-MM-DD-<task-name>.md
```

Example:

```text
./docs/superskill/task/2026-10-07-authentication-refactor.md
```

The date MUST use the `YYYY-MM-DD` format.

Do NOT use:

```text
20261007-authentication-refactor.md
07-10-2026-authentication-refactor.md
120261007-authentication-refactor.md
```

The task name SHOULD be descriptive, concise, and derived from the primary purpose of the execution.

---

# 3. Task File Creation

Before beginning the first exploration phase, the Orchestrator MUST create the task file.

The Orchestrator MUST NOT create the file and immediately delegate work.

The initial task file MUST go through a self-review process.

Required sequence:

```text
Create Draft
    ↓
Read the Task File
    ↓
Critically Review the Task File
    ↓
Identify Missing, Incorrect, or Ambiguous Information
    ↓
Edit the Task File
    ↓
Read the Task File Again
    ↓
Review Again
    ↓
Repeat Until Sufficiently Complete
    ↓
Finalize Initial Task State
    ↓
Begin Exploration
```

The Orchestrator MUST actively correct the task file rather than merely checking it.

---

# 4. Task File Self-Review

The Orchestrator MUST review the newly created task file before delegation.

The review MUST verify:

* [ ] Source document is identified
* [ ] Overall objective is explicit
* [ ] Relevant context is captured
* [ ] Actionable information is identified
* [ ] Purely informational content is separated
* [ ] Requirements are correctly interpreted
* [ ] Ambiguities are recorded
* [ ] Tasks are sufficiently decomposed
* [ ] Independent concerns are identified
* [ ] Dependencies are identified
* [ ] Each section has an owner
* [ ] Agent responsibilities are clearly bounded
* [ ] Expected outputs are defined
* [ ] Exploration requirements are identified
* [ ] Implementation requirements are identified
* [ ] Validation requirements are defined
* [ ] Completion criteria are explicit
* [ ] No critical information has been omitted

If any important information is missing or incorrect, the Orchestrator MUST edit the task file and review it again.

---

# 5. Dynamic Document Interpretation

The input document MUST be interpreted semantically.

The Orchestrator MUST NOT assume behavior based solely on the filename.

Information should be classified into appropriate categories such as:

```text
INFORMATIONAL
REQUIREMENT
ACTIONABLE
RECOMMENDATION
CONDITIONAL
AMBIGUOUS
CONSTRAINT
ACCEPTANCE_CRITERIA
```

## Informational

Information that describes the current state but does not request a change.

Example:

```text
The application currently uses JWT authentication.
```

This MUST NOT automatically become an implementation task.

## Requirement

Explicit behavior or implementation that is required.

Example:

```text
The application must rotate refresh tokens after every successful refresh.
```

This should become an actionable requirement after verification.

## Recommendation

A proposed improvement.

Example:

```text
Consider introducing refresh-token rotation.
```

The Orchestrator MUST evaluate whether the recommendation is appropriate before executing it.

## Conditional

A proposed action dependent on a condition.

Example:

```text
If the current implementation does not support X, introduce Y.
```

The condition MUST be verified first.

## Ambiguous

Information where the intended action cannot be safely determined.

The Orchestrator MUST investigate the codebase and related context before making assumptions.

---

# 6. Exploration Before Implementation

When understanding the existing codebase is necessary, exploration MUST occur before implementation.

Exploration and implementation are separate phases.

The normal workflow is:

```text
Document
    ↓
Task File
    ↓
Task File Self-Review
    ↓
Exploration
    ↓
Exploration Review
    ↓
Execution Planning
    ↓
Implementation
    ↓
Validation
```

Do NOT combine exploration and implementation merely for convenience.

---

# 7. Dynamic Subagent Allocation

The number of subagents MUST be determined dynamically according to the actual task.

When meaningful exploration is required, the Orchestrator SHOULD delegate to **two or more exploration subagents** whenever the work can be meaningfully divided.

The number of subagents should consider:

* Number of independent sections
* Complexity
* Dependencies
* Parallelizability
* Risk
* Scope
* Agent overhead
* Token efficiency

Examples:

```text
2 independent concerns
→ 2 exploration agents

4 independent concerns
→ approximately 4 exploration agents

6 independent concerns
→ approximately 6 exploration agents
```

This is not a requirement to create one agent per bullet point.

Artificial parallelism MUST be avoided.

If multiple concerns are tightly coupled, they may be assigned together.

---

# 8. Section-Based Delegation

The task MUST be divided into sections before exploration is delegated.

Each section MUST have its own:

* Section name
* Assigned agent
* Objective
* Context
* Scope
* Responsibilities
* Checklist
* Expected output
* Dependencies
* Status
* Findings
* Orchestrator review

Example:

```md
## Section 2 — Token Management

**Agent:** `token-explorer`

**Phase:** EXPLORATION

**Status:** PENDING

### Objective

Determine how tokens are currently generated, stored, refreshed,
validated, and consumed.

### Scope

Authentication and token-management components only.

### Responsibilities

- [ ] Inspect token generation
- [ ] Inspect refresh flow
- [ ] Identify affected files
- [ ] Trace dependencies
- [ ] Verify requirements
- [ ] Identify risks
- [ ] Report findings

### Expected Output

- Current implementation
- Relevant files
- Dependencies
- Required changes
- Risks
- Recommendations

### Findings

> Not started.

### Orchestrator Review

> Not reviewed.
```

A subagent MUST NOT assume ownership of the entire execution merely because it has access to the task document.

---

# 9. Exploration Agent Rules

Exploration agents MUST remain within their assigned section.

They SHOULD:

* Inspect relevant code
* Trace relevant execution paths
* Identify dependencies
* Verify document assumptions
* Identify affected files
* Identify risks
* Report evidence
* Recommend an implementation direction when appropriate

They SHOULD NOT:

* Modify unrelated code
* Implement the entire task
* Take ownership of other sections
* Declare the global task complete
* Override the Orchestrator

Unless explicitly instructed otherwise, exploration agents SHOULD NOT modify project source files.

---

# 10. Exploration Output

Exploration results SHOULD contain:

```text
Current implementation
Relevant files
Relevant symbols
Execution flow
Dependencies
Existing behavior
Verified requirements
Problems
Required changes
Risks
Recommended approach
Open questions
```

Reports should contain evidence and concrete references whenever possible.

Avoid vague conclusions such as:

```text
The authentication system probably needs changes.
```

Prefer:

```text
The refresh flow is implemented in X.
The refresh token is persisted by Y.
The middleware validates tokens through Z.
The requested rotation behavior therefore affects X, Y, and Z.
```

---

# 11. Exploration Review

After exploration agents finish, the Orchestrator MUST review their results.

The Orchestrator MUST:

1. Compare findings with the source document.
2. Compare findings with the actual codebase.
3. Identify contradictions.
4. Identify missing information.
5. Identify incorrect assumptions.
6. Resolve overlapping responsibilities.
7. Resolve cross-section dependencies.
8. Determine which recommendations are valid.
9. Update the task file.
10. Determine whether additional exploration is required.

The Orchestrator MUST NOT blindly merge subagent conclusions.

---

# 12. Exploration Completion Gate

Implementation MUST NOT begin until exploration is sufficiently complete.

The Orchestrator SHOULD verify:

```text
[ ] Relevant architecture understood
[ ] Affected files identified
[ ] Relevant execution paths understood
[ ] Dependencies identified
[ ] Existing behavior understood
[ ] Requirements verified
[ ] Risks identified
[ ] Cross-section conflicts resolved
[ ] Implementation approach established
[ ] Validation strategy established
```

If a critical item remains unresolved, the relevant task SHOULD remain blocked from implementation.

---

# 13. Execution Planning

After exploration review, the Orchestrator MUST update the task file with the implementation plan.

The plan SHOULD specify:

* Required changes
* Target components
* Reason for each change
* Dependencies
* Implementation order
* Implementation owner
* Validation requirements

The execution plan becomes the basis for implementation delegation.

---

# 14. Implementation Delegation

Implementation work SHOULD be delegated according to the finalized execution plan.

Each implementation agent MUST receive:

* Relevant source context
* Assigned section
* Verified exploration findings
* Exact responsibilities
* Constraints
* Expected outcome
* Validation requirements

Implementation agents MUST NOT unnecessarily rediscover information that has already been verified and recorded.

---

# 15. Implementation Review

When an implementation agent reports completion, the Orchestrator MUST review the result.

The Orchestrator MUST verify:

* Correct files were modified
* Changes match the requirement
* No unrelated changes were introduced
* Existing behavior was preserved where required
* Dependencies remain consistent
* Architecture remains coherent
* Diagnostics are acceptable
* Relevant tests were executed where applicable
* Validation requirements were satisfied

Only the Orchestrator may mark the corresponding task as verified complete.

---

# 16. Completion Is Evidence-Based

The following is invalid:

```text
Subagent says DONE
        ↓
[x] Task complete
```

The correct process is:

```text
Subagent reports completion
        ↓
Orchestrator reviews work
        ↓
Orchestrator verifies code
        ↓
Validation performed
        ↓
Evidence confirmed
        ↓
[x] Verified complete
```

A subagent's completion claim is only a report, not proof of completion.

---

# 17. Checklist Discipline

Use the following checklist states:

```text
[ ] Not started
[-] In progress
[x] Verified complete
[!] Blocked
[?] Needs clarification
```

The Orchestrator owns these states.

Subagents may report progress, but MUST NOT unilaterally determine that a task is globally complete.

---

# 18. Persistent State Synchronization

The Orchestrator MUST update the task file at important state transitions.

At minimum, update the task file when:

* A phase begins
* A phase ends
* An agent is assigned
* An agent finishes
* Findings are received
* Findings are verified
* A task becomes blocked
* A task is retried
* Implementation is completed
* Validation is completed
* Dependencies change
* The next action changes
* The overall status changes

The task file MUST NOT remain significantly behind the actual execution state.

---

# 19. Execution State

The task file SHOULD explicitly track the current state.

Recommended states:

```text
PENDING
ANALYZING
EXPLORATION
EXPLORATION_REVIEW
PLANNED
EXECUTION
VALIDATION
COMPLETED
```

Additional states may include:

```text
BLOCKED
FAILED
NEEDS_REVIEW
RETRYING
CANCELLED
```

The task file SHOULD always identify:

```text
Current Phase
Current Focus
Active Agent
Overall Status
Next Action
```

---

# 20. Context Compression Recovery

The task file exists specifically to protect the execution from context loss.

If the Orchestrator suspects that context has been compressed, forgotten, or partially lost, it MUST stop relying on conversational memory.

Recovery procedure:

```text
STOP
  ↓
READ ACTIVE TASK FILE
  ↓
Identify Overall Status
  ↓
Identify Current Phase
  ↓
Identify Current Focus
  ↓
Identify Completed Sections
  ↓
Identify Active Sections
  ↓
Identify Pending Sections
  ↓
Identify Blocked Sections
  ↓
Review Recent Execution Log
  ↓
Review Validation State
  ↓
Determine Next Action
  ↓
Resume Orchestration
```

The Orchestrator MUST NOT guess the current state.

---

# 21. Recovery Rule

If the Orchestrator is uncertain about:

* What is being executed
* Why it is being executed
* What has already been completed
* Which section is active
* Which agent owns a section
* Whether an implementation was validated
* What remains
* What should happen next

the Orchestrator MUST read the active task file before proceeding.

---

# 22. Failure Handling

If a subagent fails, the Orchestrator MUST record the failure.

The Orchestrator should determine:

* Cause
* Impact
* Whether retry is appropriate
* Whether additional context is required
* Whether another agent should take over
* Whether the section should become blocked

A failed task MUST NOT silently be treated as successful.

---

# 23. Blocked Tasks

If a task cannot safely continue, mark it:

```text
[!] BLOCKED
```

Record:

* Blocking reason
* Missing information
* Dependency
* Required decision
* Recommended next action

The Orchestrator MUST NOT fabricate information merely to unblock execution.

---

# 24. Ambiguity Handling

When requirements are ambiguous:

1. Identify the ambiguity.
2. Investigate the actual codebase.
3. Inspect related source context.
4. Inspect dependencies.
5. Determine whether the ambiguity can be safely resolved.
6. Record the interpretation in the task file.

If a high-impact ambiguity cannot be safely resolved:

```text
[?] NEEDS CLARIFICATION
```

Do not silently make a high-impact assumption.

---

# 25. Conflict Resolution

When the document, codebase, or subagents disagree, the Orchestrator MUST investigate the conflict.

A reasonable evidence hierarchy is:

```text
Actual Codebase
        ↓
Explicit Requirements
        ↓
Verified Architectural Constraints
        ↓
Verified Exploration Findings
        ↓
Recommendations
        ↓
Unverified Assumptions
```

This hierarchy is guidance, not permission to ignore explicit requirements.

Conflicts MUST be documented when they materially affect implementation.

---

# 26. Parallel Execution

Parallel execution SHOULD be used when work is:

* Independent
* Clearly scoped
* Non-conflicting
* Safe to execute concurrently

Examples:

```text
Independent exploration
→ Parallel

Independent implementation in isolated modules
→ Potentially parallel

Multiple agents modifying the same core file
→ Prefer controlled sequential execution
```

The Orchestrator MUST prioritize correctness over parallelism.

---

# 27. Cross-Section Dependencies

The Orchestrator MUST track dependencies between sections.

Example:

```text
Authentication
      ↓
Token Management
      ↓
Middleware
      ↓
API Handlers
      ↓
Tests
```

A dependent section MUST NOT be executed prematurely when required information is unavailable.

---

# 28. Task File Template

The following template SHOULD be used as the default structure:

```md
# File Execution Task

## 1. Task Metadata

- Task ID:
- Source Document:
- Created:
- Last Updated:
- Overall Status:
- Current Phase:
- Current Focus:
- Next Action:

---

## 2. Global Objective

...

---

## 3. Source Context

### Source Documents

- ...

### Relevant Context

- ...

---

## 4. Actionable Requirements

- [ ] ...

---

## 5. Informational Context

- ...

---

## 6. Constraints

- ...

---

## 7. Execution Rules

- [ ] Verify assumptions against the codebase
- [ ] Preserve existing behavior unless change is required
- [ ] Validate meaningful implementation changes
- [ ] Do not mark tasks complete without evidence

---

## 8. Task Decomposition

### Section 1 — <Section Name>

**Agent:** `<agent-name>`

**Phase:** EXPLORATION

**Status:** PENDING

#### Objective

...

#### Context

...

#### Scope

...

#### Responsibilities

- [ ] ...
- [ ] ...
- [ ] ...

#### Expected Output

- ...

#### Dependencies

- ...

#### Findings

> Not started.

#### Orchestrator Review

> Not reviewed.

---

### Section 2 — <Section Name>

**Agent:** `<agent-name>`

...

---

## 9. Cross-Section Dependencies

| From | To | Dependency | Status |
|------|----|------------|--------|
| ... | ... | ... | ... |

---

## 10. Execution Plan

- [ ] ...

---

## 11. Validation

- [ ] Requirements verified
- [ ] Diagnostics checked
- [ ] Tests executed where applicable
- [ ] Linter checked where applicable
- [ ] Build verified where applicable
- [ ] Integration verified where applicable
- [ ] No unintended changes detected

---

## 12. Execution Log

### YYYY-MM-DD HH:mm

- ...

---

## 13. Final Result

**Status:** ...

### Summary

...

### Completed Sections

- [x] ...

### Validation Evidence

...

### Remaining Issues

...
```

The template may be extended when the task requires additional information.

---

# 29. Execution Log

The execution log should capture important state transitions.

Example:

```md
## Execution Log

### 2026-10-07 20:15

- Created task state.
- Completed task-file self-review.
- Assigned three exploration agents.

### 2026-10-07 20:28

- Authentication exploration completed.
- Findings reviewed and verified.

### 2026-10-07 20:41

- Implementation phase started.
- Authentication implementation delegated.

### 2026-10-07 21:05

- Authentication implementation reviewed.
- Diagnostics passed.
```

Do not fill the log with insignificant actions.

---

# 30. Validation

Every meaningful implementation change MUST be validated using appropriate project tooling.

Possible validation mechanisms include:

* LSP diagnostics
* Native language tooling
* Type checking
* Linters
* Unit tests
* Integration tests
* Build
* Static analysis
* Existing project validation
* Runtime checks

The validation strategy MUST be appropriate to the actual project.

Do not claim that a validation step passed without evidence.

---

# 31. Completion Criteria

The overall task may only be marked:

```text
COMPLETED
```

when:

```text
[ ] All required sections are completed
[ ] Required implementation changes are completed
[ ] Important findings are addressed
[ ] Required dependencies are resolved
[ ] Relevant validation has been performed
[ ] No critical unresolved issue remains
[ ] Task file accurately reflects the final state
```

The task file MUST be updated with the final state before execution ends.

---

# 32. Final Result

The final task state SHOULD contain:

```md
## Final Result

**Status:** COMPLETED

### Summary

Describe what was implemented.

### Completed Sections

- [x] Section 1
- [x] Section 2
- [x] Section 3

### Validation Evidence

- Diagnostics: Passed
- Tests: Passed
- Build: Passed

### Remaining Issues

None.
```

If unresolved issues remain, the task MUST accurately report them.

Never claim complete success when critical work remains.

---

# 33. Anti-Patterns

## Immediate Execution

```text
Read document
→ Immediately edit code
```

Avoid skipping exploration when codebase understanding is required.

## Blind Execution

```text
Document says X
→ Execute X without verification
```

Never blindly follow the input document.

## Main Agent Doing Everything

```text
Main Agent
→ Explore
→ Implement
→ Test
→ Review
```

Avoid unnecessary worker-level execution.

## Subagent Doing Everything

```text
Subagent
→ Understand everything
→ Decide everything
→ Modify everything
```

Subagents must remain within their assigned responsibilities.

## Unverified Completion

```text
Agent says DONE
→ Mark [x]
```

Never do this.

## Stale Task State

```text
Actual execution changes
→ Task file remains unchanged
```

Keep persistent state synchronized.

## Artificial Parallelism

Do not create agents merely because parallelism is available.

## Guessing After Context Loss

Do not reconstruct critical execution state from memory when the task file exists.

---

# 34. Orchestrator Operating Loop

The complete operating loop is:

```text
1. Identify the input document
2. Understand its intent
3. Create the persistent task file
4. Read and review the task file
5. Correct the task file
6. Repeat review until sufficiently complete
7. Decompose the task into sections
8. Determine whether exploration is required
9. Dynamically allocate exploration agents
10. Delegate exploration
11. Collect exploration results
12. Review and verify findings
13. Resolve conflicts and dependencies
14. Update the task file
15. Create the implementation plan
16. Delegate implementation
17. Review implementation results
18. Validate changes
19. Update persistent task state
20. Repeat unresolved work when necessary
21. Verify completion criteria
22. Record final state
23. End execution
```

---

# 35. Orchestrator Recovery Mental Model

At any point, the Orchestrator MUST be able to determine:

```text
What am I executing?
Why am I executing it?
What does the source document require?
What has been verified?
What is currently being worked on?
Who owns each section?
What remains?
What is blocked?
What evidence supports completion?
What should happen next?
```

If these answers are not confidently available from current context, read the persistent task file before continuing.

---

# 36. Fundamental Rule

The entire skill can be reduced to the following operating principle:

> **Read the document. Understand the intent. Persist the task. Review the task. Delegate exploration. Verify the findings. Persist the decisions. Delegate implementation. Verify the implementation. Validate the result. Persist the final state. Recover through the task file whenever context is lost. Never rely on memory when state can be recorded.**

The Main Agent is the Orchestrator.

Subagents perform delegated work.

The task file preserves the state.
