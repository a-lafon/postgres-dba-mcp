You are checking one answer that an AI assistant gave to a question about a PostgreSQL database. The assistant queried the database through MCP tools. Below you see the question, the reference facts (computed directly from the database, and true), every tool call the assistant made with its result, and its final answer. A tool result longer than 2400 characters was shortened by the benchmark, not by the tool: the middle is replaced by a line "[... N characters cut by the benchmark, not by the tool ...]". That line is never evidence of an error or of a tool truncation; the full result was what the assistant saw.

Your job is to find where each error of the answer comes from. Do this in two steps:

1. Check every specific claim of the final answer (numbers, names, ids, counts, ranking, conclusions) against the tool results and the reference facts.
2. Classify each error you find, and each error in the list "Errors reported by a first reviewer" below, with one origin:
   - "tool": the tool output was wrong, ambiguous or lossy (a count, a size or a flag hidden, a value cut or rounded, a misleading label) and the answer relayed it faithfully. The fault is in the tool, not in the assistant.
   - "misread": the tool output says something different from what the answer states; the assistant misread or misreported it.
   - "unsupported": nothing in the tool results or in the reference facts supports the claim.
   - "none": on reading the tool results, this is not an error after all (the first reviewer only saw the final answer). An omission or a shortening that the answer states openly is not an error.

Do not speculate: classify an error as "tool" or "misread" only when a tool result shows it. A claim that the reference facts contradict is an error. A claim that the facts neither confirm nor contradict is an error only when the tool results do not support it. Do not report style, length or missing detail as errors.

For "tool" and "misread", give the number of the tool call whose result is the source (the first call number below is 1). For "unsupported" and "none", use 0.

## Question

{{.Question}}

## Reference facts

{{.Facts}}

## Tool calls

{{.Calls}}

## Final answer

{{.Answer}}

## Errors reported by a first reviewer

{{.Claimed}}

## What to produce

Reply with one JSON object and nothing else, no code fence, in exactly this shape, with an empty list when the answer has no error and the first reviewer reported none:

{"errors": [{"claim": "short quote or paraphrase of the error", "origin": "tool", "call": 1, "why": "one sentence"}]}
