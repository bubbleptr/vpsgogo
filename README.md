# VPSGoGo

Open-source desktop app for people who just bought a VPS. Paste the IP and root password once; VPSGoGo hardens the server, monitors it, reminds you before it expires, and installs common services with AI skills you approve step by step.

Status: early development; most of what follows is the design, not yet built. See [docs/SPEC.md](docs/SPEC.md).

## How it works

- **Desktop app** ([mygo](https://github.com/egoist/mygo), native UI in Go) talks to your servers over SSH. Keys and tokens stay in your system keychain.
- **gogo-agent** runs on each VPS. It opens no ports; it collects metrics and sends alerts and renewal reminders (Telegram, email, webhook, ntfy) even when your computer is off.
- **Skills** will install services such as Docker, Caddy and Uptime Kuma: Claude drafts a plan, you approve it, and every command is logged.

## Bring your own key

Skills call the Anthropic API with **your own API key**, entered in Settings and kept in the keychain. VPSGoGo runs no server of its own and never sees your key, your servers or your bills. Command output will be redacted before it is sent to Claude (docs/SPEC.md §5.1).

## Development

Requires Go 1.27.3+ and macOS for now. The UI is native, written in Go with mygo's `ui` package; there is no JavaScript toolchain.

```bash
cd apps/desktop
go tool mygo dev     # the app, with live reload
go test ./...        # views run headless through ui.Tester
go tool mygo build   # package the app
```

Check a milestone: `scripts/verify.sh M0`.

## License

[MIT](LICENSE)
