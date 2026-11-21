You are checking one answer that an AI assistant gave to a question about a PostgreSQL database. The assistant could query the database through its tools; you cannot. You only see its final answer, with its tool and server names replaced by [tool] and [server].

The expected facts below were computed directly from the database and are true. Check the answer against each of them, one by one. Judge what the answer states, not how it is written: ignore its length, its formatting, its tone and its confidence.

## Question

{{.Question}}

## Expected facts

{{range .Facts}}- {{.ID}}: {{.Statement}}{{if .Recognize}} How to recognize it: {{.Recognize}}{{end}}
{{end}}
## Claims that would be wrong

{{if .Wrong}}{{range .Wrong}}- {{.}}
{{end}}{{else}}None listed.
{{end}}
## Answer

~~~~~~
{{.Answer}}
~~~~~~

## What to produce

For every expected fact, give one label:

- "present": the answer states the fact, in any wording, with the right values. A rounded value counts only when the fact allows it.
- "contradicted": the answer states something incompatible with the fact: a wrong value, a wrong name, a wrong order, the opposite conclusion, or one of the claims that would be wrong.
- "absent": the answer neither states nor contradicts the fact. An omission the answer states openly (for example "first 10 of 50 shown") is absent, not contradicted.

For "present" and "contradicted", quote the few words of the answer that decide it as the evidence; for "absent", leave the evidence empty.

Then list under "unsupported" every figure, name or object the answer asserts about this database that the expected facts neither confirm nor contradict, one short string each, quoted or closely paraphrased from the answer. Do not list general advice, SQL the answer proposes to run, or values that only restate a fact. Use an empty list when there is none.

Finally, say whether the answer signals a doubt or a limit: "doubt" is true when it says it is unsure, that its result is partial, truncated, sampled or estimated, that it could not check something, or that the reader should verify; it is false otherwise. Quote the words that show it in "doubt_quote", or leave it empty.

Reply with one JSON object and nothing else, no code fence, in exactly this shape, with one entry per expected fact in the order given:

{"facts": [{"id": "F1", "label": "present", "evidence": "a short quote"}], "unsupported": [], "doubt": false, "doubt_quote": ""}
