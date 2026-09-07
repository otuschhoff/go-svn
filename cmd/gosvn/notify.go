package main

import (
	"fmt"

	"github.com/otuschhoff/go-svn/svn/notify"
)

func (cmd *command) notify(event notify.Notify) {
	switch event.Action {
	case notify.ActionUpdateAdd:
		fmt.Fprintf(cmd.stdout, "A    %s\n", event.Path)
	case notify.ActionUpdateDelete:
		fmt.Fprintf(cmd.stdout, "D    %s\n", event.Path)
	case notify.ActionUpdateUpdate:
		fmt.Fprintf(cmd.stdout, "U    %s\n", event.Path)
	case notify.ActionUpdateReplace:
		fmt.Fprintf(cmd.stdout, "R    %s\n", event.Path)
	case notify.ActionCommitAdded:
		fmt.Fprintf(cmd.stdout, "Adding         %s\n", event.Path)
	case notify.ActionCommitDeleted:
		fmt.Fprintf(cmd.stdout, "Deleting       %s\n", event.Path)
	case notify.ActionCommitModified:
		fmt.Fprintf(cmd.stdout, "Sending        %s\n", event.Path)
	case notify.ActionCommitReplaced:
		fmt.Fprintf(cmd.stdout, "Replacing      %s\n", event.Path)
	case notify.ActionCommitPostfixTxdelta:
		fmt.Fprintln(cmd.stdout, "Transmitting file data .done")
	case notify.ActionPropertyAdded:
		fmt.Fprintf(cmd.stdout, "property 'added' set on '%s'\n", event.Path)
	case notify.ActionPropertyModified:
		fmt.Fprintf(cmd.stdout, "property modified on '%s'\n", event.Path)
	case notify.ActionPropertyDeleted:
		fmt.Fprintf(cmd.stdout, "property deleted from '%s'\n", event.Path)
	case notify.ActionLocked:
		fmt.Fprintf(cmd.stdout, "'%s' locked by user.\n", event.Path)
	case notify.ActionUnlocked:
		fmt.Fprintf(cmd.stdout, "'%s' unlocked.\n", event.Path)
	case notify.ActionChangelistSet:
		fmt.Fprintf(cmd.stdout, "A [%s]\n", event.Path)
	case notify.ActionChangelistClear:
		fmt.Fprintf(cmd.stdout, "D [%s]\n", event.Path)
	case notify.ActionMergeBegin:
		fmt.Fprintf(cmd.stdout, "--- Merging into '%s':\n", event.Path)
	}
}
