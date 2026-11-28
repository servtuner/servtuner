# servtuner launcher as a service

`servtuner launcher` runs on a GPU host outside Kubernetes and starts and stops
`vllm serve` for the api.

It runs SGLang too when `sglang` (`--sglang`) names the Python of SGLang's virtualenv:
`PUT /server` says which server to start (`{"server": "vllm"|"sglang", "args", "env"}`), and
`GET /server/version?server=sglang` reads the installed SGLang's version. SGLang reads no API key from
its environment, so the launcher writes the key to `sglang-api-key.yaml` (0600) in `log_dir` and passes
it as `--config`: the key is never in the process's argv.

Two systemd units keep it running: it starts at boot, restarts if it fails,
and logs to the journal. A stop (or a reboot) stops vLLM first, so the GPU is
freed.

| Unit | For | Config | Secrets |
|---|---|---|---|
| `servtuner-launcher-system.service` | a bare-metal host: a system service, user `servtuner` | `/etc/servtuner/launcher.yaml` | systemd credentials (`LoadCredential=`) |
| `servtuner-launcher.service` | a workstation or WSL2: a user service | `~/.config/servtuner/launcher.yaml` | files in `~/servtuner-launcher` |

## The config file

`launcher.yaml` here is a commented example. Every key matches a flag
(`listen`, `tls`, `client_auth`, `tls_cert_file`, `tls_key_file`, `tls_client_ca_file`, `token_file`,
`api_key_file`, `vllm`, `sglang`, `log_dir`), and a flag on the command line wins.
Unknown keys are refused. The launcher reads the first of:

1. `--config <file>`,
2. `$SERVTUNER_LAUNCHER_CONFIG`,
3. `$XDG_CONFIG_HOME/servtuner/launcher.yaml` (`~/.config/servtuner/launcher.yaml`
   when unset),
4. `/etc/servtuner/launcher.yaml`.

A file named by 1 or 2 must exist; 3 and 4 are skipped when absent. The
config never holds a secret, only the files that do. Under systemd, leave
`token_file` and `api_key_file` unset and give the unit
`LoadCredential=launcher-token:...` (and `vllm-api-key:...`): they default to
`$CREDENTIALS_DIRECTORY`, a directory only that service can read, which
systemd creates at start and removes at stop. (`$XDG_RUNTIME_DIR` is not used
for the config: it is a tmpfs that is emptied at logout and reboot, and a
system service has none.)

## Install as a system service (bare metal)

```
sudo useradd --system --create-home --home-dir /var/lib/servtuner-launcher servtuner
sudo usermod -aG video,render servtuner        # GPU device access, as your distribution names it
sudo install -m 0755 servtuner /usr/local/bin/servtuner
sudo install -d -m 0750 -o root -g servtuner /etc/servtuner
sudo install -m 0640 -o root -g servtuner launcher.yaml /etc/servtuner/launcher.yaml   # then edit vllm: and listen:
sudo install -m 0600 /dev/stdin /etc/servtuner/launcher-token  <<< "<the launcher token>"
sudo install -m 0644 servtuner-launcher-system.service /etc/systemd/system/servtuner-launcher.service
sudo systemctl daemon-reload && sudo systemctl enable --now servtuner-launcher
```

For vLLM's API key, put it in `/etc/servtuner/vllm-api-key` (0600) and
uncomment its `LoadCredential=` line. Logs: `journalctl -u servtuner-launcher -f`;
The server's own log is `/var/lib/servtuner-launcher/server.log`, which the api's log
panel reads.

## Install as a user service (a workstation, WSL2)

1. Put the files in `~/servtuner-launcher` (each secret file 0600, one line):

   ```
   ~/servtuner-launcher/servtuner      # the servtuner binary (linux/amd64)
   ~/servtuner-launcher/token          # the launcher's bearer token (the api's launcher token Secret)
   ~/servtuner-launcher/vllm-api-key   # vLLM's API key (the api's vLLM key Secret)
   ```

2. Write the config, from the example (`token_file`, `api_key_file` and `log_dir`
   under `~/servtuner-launcher`; `vllm` the full path when it is in a virtualenv):

   ```
   mkdir -p ~/.config/servtuner && cp launcher.yaml ~/.config/servtuner/launcher.yaml
   ```

3. Install and start the unit:

   ```
   mkdir -p ~/.config/systemd/user
   cp servtuner-launcher.service ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now servtuner-launcher
   ```

4. Keep it running without a login session: `sudo loginctl enable-linger $USER`.

5. Check it: `systemctl --user status servtuner-launcher`, and its log with
   `journalctl --user -u servtuner-launcher -f` (vLLM's own log stays in
   `~/servtuner-launcher/server.log`, which the api's log panel reads).

## WSL2

- systemd must be on: `/etc/wsl.conf` has `[boot]` `systemd=true` (then
  `wsl --shutdown` from Windows once).
- WSL starts when Windows logs in or when something opens it, not at Windows
  boot: start it with a Task Scheduler task running `wsl.exe -d <distro>
  --exec /bin/true` at log on if the host must come back unattended.
- The launcher and vLLM listen inside WSL; reach them from the LAN through a
  Windows port proxy to `127.0.0.1`, which WSL's localhost forwarding carries
  into WSL. **Each proxy must forward to a different port than it listens on:**
  WSL forwards a Linux listener by opening the same port on Windows'
  `127.0.0.1`, which it cannot do while the proxy holds that port on
  `0.0.0.0`. A launcher restarted after its proxy existed was unreachable
  until it moved. So in the config:

  ```
  listen: 127.0.0.1:9124   # the proxy listens on 9123 and forwards here
  ```

  and, in an administrator PowerShell:

  ```
  netsh interface portproxy add v4tov4 listenport=9123 listenaddress=0.0.0.0 connectport=9124 connectaddress=127.0.0.1
  netsh interface portproxy add v4tov4 listenport=8080 listenaddress=0.0.0.0 connectport=8000 connectaddress=127.0.0.1
  ```

  servtuner's managed target then uses port 9123 for the launcher and 8080 for
  vLLM's endpoint. Allow both in the Windows firewall. Mirrored networking
  (`networkingMode=mirrored` in `.wslconfig`) removes the proxies instead.

## TLS

The launcher serves plain HTTP with `tls: false` (`--tls=false`), and TLS with
`tls: true` plus its own certificate: `tls_cert_file` and `tls_key_file`
(`--tls-cert-file`, `--tls-key-file`). With TLS, `client_auth` (`--client-auth`)
sets client certificates: `none`, `optional` (a certificate that is sent is
verified against `tls_client_ca_file`) or `required`. servtuner's api presents
no client certificate to launchers yet, so use `none`, or `optional` if other
clients have one; `required` would refuse the api. There is no skip-verify on
the api side.

servtuner 0.1's `tls_mode` was replaced: `platform` is `tls: false`, and `tls`
is `tls: true` with `client_auth: none`.
