# Security policy

Flodni is a torrent engine behind a FUSE virtual filesystem, run by its
maintainer at home. Its configuration file holds tracker and API
credentials; it holds no other personal data.

## Reporting a vulnerability

Report it privately through GitHub: **Security** tab, then **Report a
vulnerability**. Please do not open a public issue. Expect an answer within
a week.

In scope: this repository's code, the container image, CI workflows and
anything that could leak a secret. Issues in the vendored
`anacrolix/torrent` are best reported to that project as well.

## Known limits

The control panel and its APIs have no authentication. Bind them to loopback
or a private network, never to an address other people can reach.

## Secrets

The repository holds no secrets; `config.json` is ignored by git and
`config.json.example` carries placeholders. CI runs gitleaks on the full
history, and GitHub secret scanning with push protection is enabled.
