# ADR-0005: Automated submission only through authorized APIs; handoff elsewhere

**Status:** accepted (2026-11-20)

**Context.** The candidate's preferred sources are LinkedIn and ITviec (candidates: CareerViet, VietnamWorks). The existing driver submits through logged-in automated browsers and imports stealth helpers. Automation terms for these platforms were **not verified as permitting this** (LinkedIn's agreement could not be fetched by automation; ITviec's terms page returned 403; the others are unreviewed). Greenhouse and Lever application APIs need the employer's key.

**Decision.**
- Source policies are explicit data (`SourcePolicy.capabilities`), with a method and a basis per capability. Discovery and submission are separate grants.
- Submission is automated only for an `authorized_api` grant, with `automated_submit=true`, a connected account holding a credential, and passing checks. Today only the mock ATS qualifies.
- LinkedIn, ITviec, CareerViet and VietnamWorks use **handoff**: the system prepares everything and the candidate submits. The platforms' own confirmation emails are the verification (LinkedIn by job id, ITviec by canonical title+company).
- The API refuses to store platform passwords or sessions for sources without an authorized-API grant.
- No stealth, CAPTCHA/MFA handling, proxy rotation or account rotation exists in this package.

**Consequences.** + Every SUBMITTED state has a receipt, from an ATS record or the platform's own email. + Changing a platform to automated is a reviewed code change with recorded evidence. − Applying on LinkedIn/ITviec remains a human click; the system minimizes the work around it.
