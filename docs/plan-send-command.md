# Plan: `send` command — SMTP delivery for higgs

**Status:** Plan  
**Author:** akeemjenkins / Hermes  
**Date:** 2026-06-09  
**Bug:** higgs can compose messages (`draft`) but cannot send them. The
`draft` command short text literally says "does NOT send" and the internal
`smtp` package already contains `Send()`, `Config`, and `ConfigFromEnv()` —
the SMTP leg is built but the CLI surface doesn't expose it.

**Motivating scenario:** An LLM agent driving higgs was asked to respond
to a meeting invite (an `.ics`/evite email) and send the reply. It could
`fetch-and-parse` the invite and `draft` a response, but had no way to
actually deliver it — `draft` only APPENDs to Drafts. The agent hit a dead
end. `send` closes that loop so the agent can read an invite and reply to the
organizer in one tool sequence.

## What already exists

| Layer | What | File |
|-------|------|------|
| CLI | `draft` command — parses flags, builds envelope, APPENDs to IMAP | `cmd/higgs/cmd_draft.go` |
| SMTP | `smtp.Build(env)` — produces RFC5322 bytes | `internal/smtp/smtp.go` |
| SMTP | `smtp.Send(cfg, from, to, msg)` — delivers via `net/smtp` with PLAIN auth | `internal/smtp/smtp.go` |
| SMTP | `smtp.ConfigFromEnv()` — reads PM_SMTP_HOST/PORT/USERNAME/PASSWORD | `internal/smtp/smtp.go` |
| SMTP | `smtp.Config` — host/port/user/pass struct | `internal/smtp/smtp.go` |
| Tests | Draft dry-run, IMAP APPEND, In-Reply-To, validation tests | `cmd/higgs/cmd_draft_test.go` |
| Tests | SMTP Build, Send, header injection, ConfigFromEnv tests | `internal/smtp/smtp_test.go` |
| Wiring | `newRootCmd()` registers 27 subcommands | `cmd/higgs/main.go` |

## What needs to change

### Step 1: Extract shared envelope-building into `cmd/higgs/shared.go`

The envelope-building logic in `cmdDraft` (lines 67–131) is identical to
what `send` needs. Extract into a free function:

```go
// buildEnvelope parses flags, validates, resolves --in-reply-to,
// builds the smtp.Envelope, and returns the raw RFC5322 bytes.
func buildEnvelope(f *draftFlags, cfg config.Config) (*smtp.Envelope, []byte, error)
```

Both `cmdDraft` and `cmdSend` call this, then diverge: draft APPENDs to
IMAP, send calls `smtp.Send()`.

`draftFlags` already contains every field `send` needs. Rename to
`composeFlags` or keep as-is — the flag set is identical.

### Step 2: Add `cmd_send.go`

New file, modeled directly on `cmd_draft.go`. Changes from draft:

- Command name: `send`
- Short: `"Compose and send a message via SMTP"`
- Long: documents the SMTP delivery path
- Flags: **identical** to draft except:
  - Keep `--dry-run` for parity — emits the composed envelope preview
    without connecting to SMTP (lets an agent inspect the reply before
    committing to delivery)
  - Drop `--drafts-mailbox` (irrelevant for send)
  - Add `--save-to-sent` flag (bool, **default `false`**) — APPEND a copy to
    a Sent mailbox after successful delivery. **Default is false because
    Proton Mail Bridge already auto-saves SMTP-sent mail to Sent**; enabling
    this against Bridge would create duplicate Sent entries. The flag exists
    for non-Bridge SMTP servers that don't auto-file sent mail.
  - Add `--sent-mailbox` flag (string, default `"Sent"`)
- Exit codes: `0,1,2,3,4,5,9`
  - `0` ok
  - `1` api — **SMTP delivery failure** (`smtp.Send` error, via `cerr.API`)
  - `2` auth — IMAP dial failure (only when `--in-reply-to` or
    `--save-to-sent` requires an IMAP connection)
  - `3` validation — missing recipient/body, bad UID, header injection
  - `4` config — SMTP (or IMAP, for the optional paths) not configured
  - `5` imap — APPEND to Sent failed (only with `--save-to-sent`)
  - `9` internal — body read/`os` errors
  - **Note:** exit `6` is `classify`, NOT SMTP — do not use it here. SMTP
    failures map to `1 api` because `cerr` has no dedicated SMTP kind (see
    "Error-kind decision" below).
- RunE: calls `buildEnvelope()`, then `smtp.ConfigFromEnv()`, then
  `smtp.Send()`, then optionally IMAP APPEND to Sent

```go
func cmdSend(f *draftFlags, saveToSent bool, sentMailbox string) error {
    cfg, err := config.LoadFromEnv()
    // ... validation ...
    env, raw, err := buildEnvelope(f, cfg)
    // ...
    smtpCfg, ok := smtp.ConfigFromEnv()
    if !ok {
        return cerr.Config("SMTP not configured: set PM_SMTP_HOST and PM_SMTP_PORT")
    }
    recipients := append(append([]string{}, env.To...), env.Cc...)
    recipients = append(recipients, env.Bcc...)
    if err := smtp.Send(smtpCfg, env.From, recipients, raw); err != nil {
        // KindAPI -> exit code 1. This is the SMTP delivery failure path.
        return cerr.API(502, "smtpError", err.Error(), "")
    }
    // Optionally save to Sent (default OFF; Proton Bridge auto-saves).
    if saveToSent {
        // IMAP APPEND to sentMailbox (reuse draft's APPEND logic).
        // Errors here are cerr.Auth (2) on dial / cerr.IMAP (5) on APPEND.
    }
    // Output JSON result: {"sent": true, "message_id": ..., "recipients": N}
}
```

### Error-kind decision: SMTP failures use `KindAPI` (exit 1)

`cerr` has no dedicated SMTP kind. Rather than add one (which would touch
`internal/cerr/errors.go`, `ExitCodeDocs`, the README exit-code table, and
every consumer's mental model), `send` maps SMTP delivery failures to the
existing `KindAPI` (exit code `1`) via `cerr.API(502, "smtpError", ...)`. The
`reason` field is set to `"smtpError"` so an agent can still distinguish SMTP
failures from other api errors by branching on `.error.reason`, while
`.error.kind`/exit code stays within the published enum.

This keeps the 1:1 exit-code↔kind contract intact and adds no new exit codes
to the schema. If SMTP becomes a first-class concern later, promoting it to a
dedicated `KindSMTP` is a separate, deliberate change.

### Step 3: Register in `main.go`

Add `newSendCmd()` to the `root.AddCommand(...)` call in `newRootCmd()`.

### Step 4: Register in schema

Ensure `cmd/higgs/cmd_schema.go` picks up the new command
automatically (cobra commands are discoverable — verify).

### Step 5: Tests — `cmd_send_test.go`

Mirror the draft test patterns:
- `TestSendCmdFlagsAndAnnotations` — verify flags, annotations, and that
  `exit_codes` is exactly `"0,1,2,3,4,5,9"`
- `TestSendValidationNoRecipient` — no --to flag → validation error (3)
- `TestSendValidationNoBody` — no --body-file → validation error (3)
- `TestSendMissingSMTPConfig` — PM_SMTP_HOST unset → config error (4)
- `TestSendDryRun` — --dry-run emits envelope preview without connecting to SMTP
- `TestSendToFakeSMTPServer` — start a fake SMTP server, verify delivery
- `TestSendInReplyTo` — --in-reply-to resolves source message headers (the
  meeting-evite reply path: fetch organizer's Message-ID, set In-Reply-To)
- `TestSendSavesToSent` — run with `--save-to-sent` (it defaults to false),
  verify IMAP APPEND to the Sent folder
- `TestSendSmtpFailureIsExitCode1` — point at a closed/refusing port, assert
  `cerr.From(err).Kind == cerr.KindAPI` and `reason == "smtpError"`

The fake SMTP server must speak the full `net/smtp` client handshake, not
just read DATA: `net/smtp.SendMail` issues `EHLO` → (optional `STARTTLS`) →
(optional `AUTH`) → `MAIL FROM` → `RCPT TO` → `DATA` → `QUIT`, and the stub
must reply to each verb (220 greeting, 250 to EHLO/MAIL/RCPT, 354 to DATA,
250 after the message, 221 to QUIT). Drive the test with **no auth** (empty
`PM_SMTP_USERNAME`) so the client skips `AUTH` — note that `net/smtp` refuses
`PLAIN` auth over a non-TLS connection unless the host is `localhost`, so any
auth-bearing test must bind to loopback. Use the existing `imaptest`
framework for the optional Sent-APPEND side and a `net.Listen` stub for SMTP.

### Step 6: Update docs

- `README.md` — the `## Commands` section is a **curated subset** (it
  documents `classify`/`apply-labels`/`state`/etc. but not `draft`,
  `archive`, `move`, …). The canonical, complete command list is the
  `schema` manifest, so documenting `send` in the README is optional polish,
  not a contract requirement. If added, also document the meeting-reply
  example (`--in-reply-to` + `--to organizer`).
- `CHANGELOG.md` — entry for the new command

## Env vars required for `send`

Core SMTP delivery path:

| Variable | Default | Description |
|----------|---------|-------------|
| `PM_SMTP_HOST` | *(required)* | SMTP server hostname |
| `PM_SMTP_PORT` | *(required)* | SMTP server port (Proton Bridge default: 1025) |
| `PM_SMTP_USERNAME` | `""` | SMTP AUTH username (empty = no auth) |
| `PM_SMTP_PASSWORD` | `""` | SMTP AUTH password |

IMAP is **also** required for several `send` paths (same vars as `draft`):

| Variable | Default | Needed when |
|----------|---------|-------------|
| `PM_IMAP_USERNAME` | *(required)* | `--from` defaulting (used as the sender when `--from` is omitted), and any IMAP path below |
| `PM_IMAP_PASSWORD` | *(required)* | `--in-reply-to` (fetches the source message) or `--save-to-sent` (APPENDs the copy) |
| `PM_IMAP_HOST` / `PM_IMAP_PORT` / … | see README | same as `draft`; only consulted on the IMAP paths |

A pure SMTP send (explicit `--from`, no `--in-reply-to`, `--save-to-sent`
off) needs only the `PM_SMTP_*` vars. The meeting-evite reply path uses
`--in-reply-to`, so it requires `PM_IMAP_*` as well.

## Constraints

- **No new dependencies.** Everything uses stdlib `net/smtp` which is
  already imported in `internal/smtp`.
- **Backward compatible.** `draft` behavior is untouched.
- **Same flag set as draft.** `send` accepts the same `--to`, `--cc`,
  `--bcc`, `--subject`, `--body-file`, `--body-html-file`, `--from`,
  `--in-reply-to`, `--source-mailbox` flags (dropping `--drafts-mailbox` and
  adding `--save-to-sent`/`--sent-mailbox`). Operators switching from `draft`
  to `send` change one word.
- **Agent contract honored.** `send` emits JSON on stdout, structured errors
  on failure, and publishes itself via `higgs schema`. SMTP failures stay
  within the published exit-code enum (`1 api`, with `reason: "smtpError"`)
  so deterministic retry/escalation still works — no out-of-band exit codes.

## Worked example: reply to a meeting invite (the motivating case)

End-to-end sequence an agent can run from the `schema` manifest:

```sh
# 1. Find the invite in the inbox.
higgs fetch-and-parse INBOX | jq 'select(.subject | test("invit|meeting"; "i"))'
# -> note the UID, e.g. 1842, and the organizer address

# 2. Compose the reply body to a file (agent writes accept/decline text).
#    Then send it as a threaded reply to the organizer.
higgs send \
  --to organizer@example.com \
  --in-reply-to 1842 \
  --source-mailbox INBOX \
  --body-file /tmp/reply.txt
# In-Reply-To/References are resolved from UID 1842; Subject gets a "Re:" prefix.
```

`send` exits `0` with `{"sent": true, "message_id": "...", "recipients": 1}`
on success, or a typed envelope (`1 api`/`2 auth`/`3 validation`/`4 config`)
the agent can branch on. With Proton Bridge, the message lands in Sent
automatically — no `--save-to-sent` needed.
