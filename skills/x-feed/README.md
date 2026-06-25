# x-feed-skills

Private skills for turning internal exploit-analysis outputs into safe, curated X and Typefully feed material.

This repository is for editorial workflows around public-facing posts derived from private TraceExp/report artifacts. It should help agents:

- sanitize private report details before any public draft is written
- preserve the meaningful exploit perspective without leaking reproduction detail
- produce concise X/Typefully-ready threads with clear claims and caveats
- create high-level diagrams that explain impact, proof boundaries, and flow without operational specificity
- render one X-ready exploit-flow card from sanitized/public report material

## Skills

- `skills/sanitize-exploit-report`: convert private report artifacts into a public-safe brief.
- `skills/draft-x-exploit-thread`: turn a sanitized brief into an X/Typefully thread.
- `skills/exploit-flow-card`: render a public-safe social card showing attack flow, the broken invariant, and selected asset movement.

## Operating Rule

Do not draft public posts or visual cards directly from private reports. First produce a sanitized brief, then write the feed draft or render the social card from that public-safe material.

Render a visual only when it carries real reasoning value: a causal hinge, state-consumer boundary, false-lead split, broken invariant, numeric gap, money-realization path, or selected asset movement. Do not render a visual whose only message is "a hack happened," the protocol name, or an unsupported loss number.
