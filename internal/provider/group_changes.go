// SPDX-License-Identifier: MPL-2.0

package provider

// groupOpKind is one kind of group write.
type groupOpKind int

const (
	opGrantPermission groupOpKind = iota
	opRevokePermission
	opAddMember
	opRemoveMember
)

// groupOp is one group write: a permission or a user name.
type groupOp struct {
	kind groupOpKind
	item string
}

// setDiff returns the values in want and not in have (add) and the values in
// have and not in want (remove), each sorted. Pure.
func setDiff(have, want []string) (add, remove []string) {
	haveSet := toSet(have)
	wantSet := toSet(want)
	for _, v := range sortedCopy(want) {
		if !haveSet[v] {
			add = append(add, v)
		}
	}
	for _, v := range sortedCopy(have) {
		if !wantSet[v] {
			remove = append(remove, v)
		}
	}
	return add, remove
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

// groupDesired is the permissions and members a group should have.
type groupDesired struct {
	permissions []string
	members     []string
}

// planGroupOps lists the writes that move a group from have to want. Grants
// come before revokes, so the group never holds zero permissions on the way.
// Pure.
func planGroupOps(have, want groupDesired) []groupOp {
	grant, revoke := setDiff(have.permissions, want.permissions)
	add, remove := setDiff(have.members, want.members)
	ops := make([]groupOp, 0, len(grant)+len(revoke)+len(add)+len(remove))
	ops = appendOps(ops, opGrantPermission, grant)
	ops = appendOps(ops, opRevokePermission, revoke)
	ops = appendOps(ops, opAddMember, add)
	ops = appendOps(ops, opRemoveMember, remove)
	return ops
}

func appendOps(ops []groupOp, kind groupOpKind, items []string) []groupOp {
	for _, item := range items {
		ops = append(ops, groupOp{kind: kind, item: item})
	}
	return ops
}
