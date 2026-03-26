package common

import (
	stdpath "path"

	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/sign"
)

func Sign(obj model.Obj, parent string, encrypt bool) string {
	if obj.IsDir() {
		return ""
	}
	// Always generate sign for files to support search download
	return sign.Sign(stdpath.Join(parent, obj.GetName()))
}
