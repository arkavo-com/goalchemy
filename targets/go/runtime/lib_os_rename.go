package rt

import nativeos "github.com/eugenioenko/goalchemy/lib/os"

func LibOsRename(oldpath string, newpath string) int { return nativeos.Rename(oldpath, newpath) }
