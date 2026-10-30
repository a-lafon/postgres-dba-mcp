You are grading two answers that two AI assistants gave to the same question about a PostgreSQL database. Each assistant could query the database through its own tools; you cannot. You only see the final answers.

The reference facts below were computed directly from the database and are true. Use them to check every claim of both answers. Do not trust a claim that the facts contradict, and do not reward a claim only because it sounds confident. A claim that the facts neither confirm nor contradict is not an error by itself; count it as an error only when it is specific and implausible given the facts.

An omission or a shortening that an answer states openly (for example "15 of the 20 rows", "output truncated", "first 10 shown") is not an error. Judge every id, name or value that an answer cites against the reference facts, not against what the answer happens to display: you cannot see the tool results, so a cited value that the facts do not contradict is not an error just because the answer does not show it.

Do not favour an answer for its length, its formatting or its position (A or B). Judge substance only.

## Question

{{.Question}}

## Reference facts

{{.Facts}}

## Answer A

{{.A}}

## Answer B

{{.B}}

## What to produce

Score each answer from 1 (poor) to 5 (excellent) on three criteria:

- correctness: are the statements true according to the facts, and does the answer reach the right conclusion?
- completeness: does the answer cover what the question asks and what the facts show is relevant, without missing an important item?
- actionability: can the reader act on it right away (exact names, ready-to-run statements, clear priorities)? For a question that only asks for information, score how directly usable the information is.

Also list every factual error or hallucination of each answer, one short string each, such as a wrong number, a name that does not exist or a conclusion the facts contradict. Use an empty list when there is none.

Then name the winner: "A" or "B" when one answer is clearly better overall, "tie" when they are about equal.

Reply with one JSON object and nothing else, no code fence, in exactly this shape:

{"a": {"correctness": 1, "completeness": 1, "actionability": 1, "errors": []}, "b": {"correctness": 1, "completeness": 1, "actionability": 1, "errors": []}, "winner": "A", "reason": "one or two sentences"}
