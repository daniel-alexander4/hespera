# hesplay bash completion — shipped to /usr/share/bash-completion/completions/hesplay
# and sourced lazily on the first `hesplay <Tab>`. Loading the script from the
# installed binary keeps it current with the CLI (no static file to drift).
source <(hesplay completion bash 2>/dev/null)
