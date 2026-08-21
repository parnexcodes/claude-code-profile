package main

import "fmt"

const bashCompletion = `# ccp bash completion — source this from ~/.bashrc
_ccp_complete() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local prev="${COMP_WORDS[COMP_CWORD-1]}"
  local subcommands="list show add edit remove proxy doctor completion version help"

  if [[ "$prev" == "proxy" ]]; then
    COMPREPLY=( $(compgen -W "status start stop restart install init logs models" -- "$cur") )
    return 0
  fi

  if (( COMP_CWORD <= 1 )); then
    local profiles
    profiles="$(ccp __profiles 2>/dev/null)"
    COMPREPLY=( $(compgen -W "$subcommands $profiles" -- "$cur") )
  fi
  return 0
}
complete -F _ccp_complete ccp
`

const zshCompletion = `#compdef ccp
# ccp zsh completion — source this from ~/.zshrc (or drop in fpath)
_ccp() {
  local -a subcommands
  subcommands=(
    'list:list profiles'
    'show:print the environment a profile applies'
    'add:create a new profile'
    'edit:edit config or a profile'
    'remove:delete a profile'
    'proxy:manage local CLIProxyAPI'
    'doctor:validate the setup'
    'completion:print shell completion'
    'version:print version'
    'help:show help'
  )
  local -a proxy_cmds
  proxy_cmds=(status start stop restart install init logs models)

  local -a profiles
  profiles=(${(f)"$(command ccp __profiles 2>/dev/null)"})

  _arguments -C \
    '(-q --quiet)'{-q,--quiet}'[suppress banner]' \
    '1:cmd:->cmd' \
    '*::arg:->args'

  case "$state" in
    cmd)
      if (( CURRENT == 1 )); then
        _describe -t commands 'command' subcommands
        _describe -t profiles 'profile' profiles
      fi
      ;;
    args)
      case "$words[1]" in
        proxy) _describe -t subcmds 'proxy command' proxy_cmds ;;
        show|edit|remove) _describe -t profiles 'profile' profiles ;;
      esac
      ;;
  esac
}
`

func printCompletion(shell string) {
	if shell == "zsh" {
		fmt.Print(zshCompletion)
	} else {
		fmt.Print(bashCompletion)
	}
}
