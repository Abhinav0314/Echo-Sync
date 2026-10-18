# Echo Sync

Authenticated Listen Together rooms for Echo 2.1.0. Based on the GPL-3.0 Metroserver playback protocol; original Git history, licence and protocol provenance are retained. Each phone plays its own audio; this service synchronises the queue, playback and membership.

## Deploy on Render

1. Create a **Web Service** from `Abhinav0314/Echo-Sync`, branch `master`.
2. Runtime **Docker**, repository root, Dockerfile `./Dockerfile`. Leave Docker command empty. Start with **Free** and one instance. Set health check path to `/health`.
3. Add these variables (or use the included `render.yaml` Blueprint):

   | Variable | Value |
   | --- | --- |
   | `SUPABASE_URL` | The same project URL used by Echo, `https://YOUR-PROJECT.supabase.co` |
   | `SUPABASE_ANON_KEY` | That project's public anon or publishable key |
   | `PORT` | `10000` |
   | `DATABASE_FILE` | `/tmp/echo-sync.db` |

   Do **not** use the Supabase service-role key, database password or JWT signing secret. No migration is needed for this service. Authentication fails closed if its project configuration is missing.

4. Deploy and check `https://YOUR-SERVICE.onrender.com/health`: it returns `{"status":"ok"}`. This is only a liveness check; it does not prove a phone can sign in or play.
5. In **Settings → Custom Domains**, add `listen.echo.vibescape.club`. At your DNS provider create a CNAME record for `listen.echo` pointing at your Render hostname, without `https://` or a path. If the DNS zone itself is `echo.vibescape.club`, use record name `listen` instead. Wait for verification and Render's TLS certificate.
6. Check `https://listen.echo.vibescape.club/health`. Echo 2.1.0 connects to **`wss://listen.echo.vibescape.club/ws`** automatically. The app deliberately never sends account credentials to arbitrary custom URLs, redirects or the default community server.
7. Before publishing the APK, test on two signed-in phones: create/open room, join by code, password missing/wrong/correct, share invitation to Standard and Vivo, host approval, play/pause/seek/next, network drop/reconnect, kick, logout and another account trying a previous session.

A nested subdomain is supported. Add the exact hostname to Render so it gets its own certificate; `*.vibescape.club` alone does not cover `listen.echo.vibescape.club`. See [Render custom domains](https://render.com/docs/custom-domains).

## Room privacy and limits

- `/ws` requires a bearer access token validated by the configured Supabase Auth `/auth/v1/user` endpoint. Issuer, authenticated audience/role, expiry, returned user ID and anonymous/banned-account state are checked. Tokens are never stored, logged or accepted in URLs. Connections close at token expiry and accounts are rechecked every minute; clients refresh/reconnect through their normal account SDK. Logout disconnects the app immediately. Revoked access tokens can remain valid until their JWT expiry according to Supabase's token lifecycle.
- There is no public room list. A room code grants a join attempt, not host permissions. Passwords are optional and checked before approval or auto-join. Hosts can require approval. Invitations carry only the room code; send a password separately.
- Passwords use random salts and PBKDF2-HMAC-SHA256 with 600,000 iterations, constant-time comparison and two concurrent hashing slots. Only hashes are persisted. Reconnect tokens use secure randomness and are bound to the verified account and original room. Kicks block that account from rejoining that room.
- Account limits: four simultaneous connections, two hosted rooms, five room creations/minute, and fifteen join/password attempts per ten minutes across sockets. Global authentication checks are capped at thirty/minute, with sixteen in flight. Frames are capped at 512 KiB, decoded payloads at 1 MiB; account/global traffic budgets, existing message rates, queue sizes and room limits also apply.
- Account verification and room isolation do not attest that the client is an unmodified official APK. User-agent strings, package names and an embedded API secret cannot prove this. Hardware/app attestation is a separate deployment requirement and is not enabled by this fork.

Use one server instance. Room state is local; multiple replicas need a shared state design. Free Render storage is ephemeral: restart, redeploy or spin-down may lose rooms and reconnect state. A health monitor does not provide persistence. For production storage use a paid instance with a writable persistent disk and change `DATABASE_FILE` accordingly (runtime UID 1000).

## Development

Go 1.27.2+, `protoc`, and `protoc-gen-go` 1.36.11:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
bash scripts/generate_proto.sh
go test -race ./...
go vet ./...
go build ./cmd/metroserver
```

`protocol/listentogether.proto` is vendored; no submodule or separate protocol repository is needed. Existing wire numbers are unchanged, with optional password and auto-join fields appended. The GitHub workflow validates the protocol, security/behaviour tests and server binary. Docker images are built by Render when you deploy, without publishing an unrelated public image or requiring Codecov credentials.

The optional upstream `UA_POLICY_FILE` can block clients, and `UA_ADMIN_TOKEN` enables the protected administrative report. Neither is required or a substitute for account authentication. Leave the admin token unset unless you use the report.
