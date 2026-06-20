[Backlight Verified Incident]

Protocol: {{ .Protocol }}
Chain: {{ .Chain }}
Tx: {{ .Tx }}

Impact:
- {{ .ImpactLoss }}
- {{ .ImpactGain }}

Root cause:
- {{ .RootCause }}

Artifacts:
- Report: {{ .ReportURL }}
- PoC: {{ .PoCURL }}

Flow:
- {{ index .Flow 0 }}
- {{ index .Flow 1 }}
- {{ index .Flow 2 }}

Attacker CA:
- Attack contract: {{ .AttackContract }}
- Attacker EOA: {{ .AttackerEOA }}

Image:
- {{ .Image }}
