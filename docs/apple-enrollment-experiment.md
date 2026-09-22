# Encrypted Apple DAV enrollment experiment

Server branch: `apple-dav-isolated`. Apps branch: `apple-dav-automatic`.
This is an opt-in prototype for native iOS/iPadOS. It is not MDM enrollment.
The existing password-free profile and manual password entry remain available.

## What changes for the user

The new **Connect without entering passwords (experimental)** button opens the
profile download directly. The user still opens system Settings, reviews the
profile, and confirms installation (possibly entering the device passcode).
Account usernames and app passwords are delivered in an encrypted profile.
Ordinary third-party iOS apps cannot silently install system accounts.

The flow is implemented and covered by protocol/API tests; **installation on a
physical iPhone has not yet been verified**. In particular, acceptance of the
self-signed profile signature and the SCEP CA fingerprint must be tested on the
target iOS version. Do not call this production-ready or silently fall back to
sending a plaintext password if enrollment fails.

## Enable on a test deployment

Build this server branch and set `DISCODRIVE_APPLE_ENROLLMENT=1` in its environment.
The Compose app already reads `.env`; recreate the app container after changing
its environment. Do not deploy unrelated working-tree changes unintentionally.
Native iOS must be built from the corresponding apps branch. This does not change
Wails, Android, or native macOS behavior.

Use a publicly trusted HTTPS origin and preserve its Host header, including a
nondefault port, through reverse proxies. Enrollment rejects a requested origin
with a different host or port. All `/apple-enrollment/` traffic must reach the
same server process. Pending enrollments expire after ten minutes or a restart.
Ensure proxies never record enrollment paths, query strings, or request bodies:
the download URL is a short-lived bearer capability. The repository nginx
configuration already disables access logs. No external SCEP service is needed.

## Exchange and security boundaries

1. The authenticated app checks `GET /me/apple-enrollment`. It returns false
   when the experiment is disabled, before any new app password is created.
2. The app reuses its Keychain DAV credential (or creates one using the existing
   device endpoint). `POST /me/apple-enrollment` validates its owner, device kind,
   password, session version, selected DAV services, and HTTPS origin.
3. A 256-bit random, bounded, ten-minute session yields a signed `Profile Service`
   bootstrap. This contains no account password, only a one-time enrollment URL.
4. The first signed response must echo the challenge. The server pins its signer
   and returns a SCEP identity request with a separate random challenge. The
   bootstrap signature proves possession, not Apple hardware attestation; device
   attributes are not trusted or used as authorization. Access comes from the
   authenticated session ticket. Keep the link private until enrollment completes.
5. iOS generates an RSA key locally. SCEP accepts only a valid CSR signed by the
   same key as the request, with the correct challenge. A session issues at most
   one key's certificate; retries reuse it. Client-requested CA privileges and
   alternative names are ignored. The ephemeral enrollment CA is not installed
   as a trusted root, and the profile requests no MDM/device-management rights.
6. The final signed request must use that exact issued certificate. The server
   serializes the DAV payload array, encrypts it with CMS AES-256-CBC for that
   certificate only, places it in `EncryptedPayloadContent`, and signs the outer
   profile with SHA-256. AES-CBC is selected once at process initialization for
   Apple compatibility, not changed concurrently per request.
7. The final encrypted response is cached for installer retries. The session's
   plaintext password buffer is cleared on delivery, cancellation, or expiry.
   Nothing is intentionally persisted to disk. This is not a promise to erase
   every runtime/crypto temporary copy from process memory.

The HTTP layer rechecks device ownership, token version, forced-password-change
state, and DAV feature availability on every exchange. Revocation cancels the
pending session. Existing system accounts retain the ordinary app-password
revocation behavior. The issuing CA currently lives only in memory and has a
one-year certificate lifetime; this prototype has no CA persistence/rotation.

The native app retains the existing credential even after a failed enrollment,
so retry/revocation remain possible. Selecting the regular connection method
continues to generate a password-free profile. Automatic setup does not change
file sync, pairing, vault credentials, or stored files.

## Validation

- Go tests exercise a complete signed bootstrap, SCEP request/reply, encrypted
  profile decryption, retry behavior, independent Apple `plutil` parsing, and OpenSSL signature/decryption checks.
- Rejection cases cover wrong challenge/signature, cross-session/cross-device
  requests, tampering, expiry, cancellation, per-account limits, foreign device
  credentials, incorrect passwords/origins, and revocation.
- Swift tests cover HTTPS-only credential submission, same-origin download URL
  validation, and disabled capability handling.
- A real-device pass must confirm that no DAV password prompt occurs and that
  both Calendar and Contacts synchronize. Verify reinstall/revoke too.

No production deployment or account migration is performed automatically.

## Sources

- [Apple: installing downloaded profiles](https://support.apple.com/en-gb/102400)
- [Apple: encrypted profile structure](https://developer.apple.com/documentation/devicemanagement/configuring-multiple-devices-using-profiles)
- [Apple: OTA profile delivery](https://developer.apple.com/library/archive/documentation/NetworkingInternet/Conceptual/iPhoneOTAConfiguration/OTASecurity/OTASecurity.html)
- [Apple: SCEP payload](https://developer.apple.com/documentation/devicemanagement/scep)
- [Smallstep SCEP implementation](https://github.com/smallstep/scep)
