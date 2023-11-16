package util

type Event int

const (
	UpdateE Event = iota
	DeleteE
)

func Ipv4Family() *int64 {
	var v int64 = 4
	return &v
}

func ClearMap(m map[string]struct{}) {
	for k := range m {
		delete(m, k)
	}
}
