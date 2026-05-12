# bash completion for o3ui.
#
# Installed by the .deb to /usr/share/bash-completion/completions/o3ui
# and sourced lazily by bash-completion on first <tab>. Profile-name
# completion is deliberately *not* attempted: openvpn3 names commonly
# contain spaces ("Frankfurt — Work") which break compgen's word-split.
# Users type a substring; o3ui resolves it.

_o3ui() {
    local cur prev words cword
    _init_completion 2>/dev/null || {
        # Fallback when bash-completion isn't present.
        cur=${COMP_WORDS[COMP_CWORD]}
        cword=$COMP_CWORD
        words=("${COMP_WORDS[@]}")
    }

    if [[ $cword -le 1 ]]; then
        COMPREPLY=($(compgen -W "status list connect disconnect desklet help -h --help" -- "$cur"))
        return
    fi

    case ${words[1]} in
    status | list)
        COMPREPLY=($(compgen -W "--json -j" -- "$cur"))
        ;;
    desklet)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "install uninstall where" -- "$cur"))
        fi
        ;;
    connect | disconnect)
        # First positional is a profile substring; offer no completions
        # so users get the file-completion fallback only if they ask.
        ;;
    esac
}
complete -F _o3ui o3ui
