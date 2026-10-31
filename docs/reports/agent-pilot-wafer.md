# Agent suite 4: a one-task pilot through Wafer

**Date:** 2026-10-31. **Suite:** `bench/agent/tasks` (suite 4,
`docs/reports/agent-tasks-v4.md`). **Authorized:** a paid pilot, by the
owner, run through Wafer's serverless API.

## What was run

- **Task.** `t01_sorted_set`.
- **Model.** `wafer:GLM-5.3`.
- **Trials.** One per setup (`structured`, `plain`).
- **Budget.** 20 iterations and 30 minutes per trial.

```bash
go run ./bench/agent/harness -models wafer:GLM-5.3 -tasks t01_sorted_set -trials 1 -kern <prebuilt> -out <scratch report>
```

The trial records are under `bench/agent/runs/20261031-070018`, which is
git-ignored. The report was written to a scratch path, so
`bench/agent/REPORT.md` still holds the committed suite-2 report.

## The harness change

A `wafer` provider (`bench/agent/harness/llm.go`). Wafer serves OpenAI's
Responses API (https://docs.wafer.ai/serverless), so the provider reuses
the OpenAI client:
- the endpoint is `/v1/responses` under the base of `WAFER_API_URL`;
- the key comes from `WAFER_API_KEY`.

Wafer requires the explicit `"type": "message"` on message input items,
which OpenAI also accepts, so the client now always sends it. One call
with a single tool was checked before the pilot.

## Results

| setup | success | iterations | wall | tokens (in / out) |
|---|---|---|---|---|
| structured | yes | 10 | 124 s | 354,698 / 23,428 |
| plain | no | 20 (budget) | 1,800 s | 223,684 / 2,647 |

With structured diagnostics the model met the task's bounds-check budget
in 10 iterations. With text diagnostics it ran out of its 20 iterations,
and wrote little: 2,647 output tokens in all.

This matches the earlier one-task pilot on the same task
(`claude-opus-5-5`, 2026-10-30): the structured setup passed in 3
iterations, and the plain setup failed at 20.

The report scores the task a loss, because a task needs at least five
trials per setup to count. One trial per setup is evidence that suite 4
separates the two setups on this task, not a result.

## Cost and the next step

The two trials used about 578,000 input and 26,000 output tokens. Suite
4 in full needs 10 tasks × 2 setups × 5 trials = 100 trials per model,
so on the order of 30 million input tokens per model, if tasks resemble
this one. The harness does not record transcripts, so a failure's
cause (here, the plain setup's near-silence) cannot be read afterwards.
Recording them before a full run would make failures explainable.
