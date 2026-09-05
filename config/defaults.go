package config

const DefaultConfig = `### Subversion client configuration
[auth]
store-passwords = yes
store-auth-creds = yes

[miscellany]
enable-auto-props = no
global-ignores = *.o *.lo *.la *.al .libs *.so *.so.[0-9]* *.a *.pyc *.pyo __pycache__
interactive-conflicts = yes

[auto-props]

[tunnels]
ssh = ssh -q --
`

const DefaultServers = `### Subversion server configuration
[groups]

[global]
http-timeout = 0
http-compression = auto
store-passwords = yes
store-plaintext-passwords = ask
ssl-authority-files =
ssl-trust-default-ca = yes
`
