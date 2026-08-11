package v1

import (
	"testing"

	model1 "github.com/NimoTech/NimoOS-LocalStorage/model"
)

func TestHasRaidDescendant(t *testing.T) {
	mdChild := model1.LSBLKModel{Name: "md127", Type: "raid10"}
	cases := []struct {
		name string
		dev  model1.LSBLKModel
		want bool
	}{
		{"member of an (unmounted) array", model1.LSBLKModel{
			Name: "sda", Children: []model1.LSBLKModel{mdChild},
		}, true},
		{"partitioned member: md under a partition", model1.LSBLKModel{
			Name: "sda", Children: []model1.LSBLKModel{
				{Name: "sda1", Type: "part", Children: []model1.LSBLKModel{mdChild}},
			},
		}, true},
		{"md recognised by name when type is unset", model1.LSBLKModel{
			Name: "sdb", Children: []model1.LSBLKModel{{Name: "md0"}},
		}, true},
		{"plain empty disk", model1.LSBLKModel{Name: "sdb"}, false},
		{"disk with ordinary partitions", model1.LSBLKModel{
			Name: "sdc", Children: []model1.LSBLKModel{
				{Name: "sdc1", Type: "part"},
				{Name: "sdc2", Type: "part"},
			},
		}, false},
	}
	for _, c := range cases {
		if got := hasRaidDescendant(c.dev); got != c.want {
			t.Errorf("%s: hasRaidDescendant = %v, want %v", c.name, got, c.want)
		}
	}
}
