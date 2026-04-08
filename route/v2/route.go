package v2

import (
	"github.com/NimoTech/NimoOS-LocalStorage/codegen"
)

type LocalStorage struct{}

func NewLocalStorage() codegen.ServerInterface {
	return &LocalStorage{}
}
