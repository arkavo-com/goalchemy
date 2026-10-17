package catalog

import "go/types"

const TaskPackage = LibModule + "task"

func TaskIntrinsic(obj types.Object) string {
	if obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == TaskPackage && (obj.Name() == "Start" || obj.Name() == "Await") {
		return obj.Name()
	}
	return ""
}

func IsFuture(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == TaskPackage && n.Obj().Name() == "Future"
}
