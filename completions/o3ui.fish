# fish completion for o3ui.
# Installed by the .deb to /usr/share/fish/vendor_completions.d/o3ui.fish.

complete -c o3ui -f

complete -c o3ui -n '__fish_use_subcommand' -a status     -d 'show active session'
complete -c o3ui -n '__fish_use_subcommand' -a list       -d 'list imported profiles'
complete -c o3ui -n '__fish_use_subcommand' -a connect    -d 'start the VPN for a profile'
complete -c o3ui -n '__fish_use_subcommand' -a disconnect -d 'tear down the active session'
complete -c o3ui -n '__fish_use_subcommand' -a desklet    -d 'manage the Cinnamon desklet'
complete -c o3ui -n '__fish_use_subcommand' -a help       -d 'usage'

complete -c o3ui -n '__fish_seen_subcommand_from status list' -l json -s j -d 'machine-readable JSON'

complete -c o3ui -n '__fish_seen_subcommand_from desklet; and not __fish_seen_subcommand_from install uninstall where' \
    -a 'install uninstall where'
