Task: Status Divergence Analysis
Explain a divergence between two recorded reading statuses and recommend a safe resolution.

Reading status lifecycle:
want-to-read -> reading -> read
read may also become dnf (did not finish); on-hold (paused) is a valid resting state between want-to-read and reading or between reading and read/dnf.

Resolution Rules:
- Treat recorded statuses and timestamps as facts. Prefer the state with stronger evidence (finished marker, later timestamp, explicit progress milestone).
- When states are equally supported or unresolvable, recommend keep_separate.
- adopt_source: source state is better supported and should be written to target.
- adopt_target: target state is better supported and source should be left alone.
- keep_separate: states conflict without sufficient resolution evidence; require reader review.
- summary: one clear sentence stating what diverged and which state is better supported.
- recommended_action: exactly one of "adopt_source", "adopt_target", "keep_separate".
- rationale: detailed reasoning naming the specific observations used.

Return only a JSON object with keys summary, recommended_action, rationale.
