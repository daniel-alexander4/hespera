# hescli bash completion — shipped to /usr/share/bash-completion/completions/hescli
# and sourced lazily on the first `hescli <Tab>`. Loading the script from the
# installed binary keeps it current with the CLI (no static file to drift).
source <(hescli completion bash 2>/dev/null)
