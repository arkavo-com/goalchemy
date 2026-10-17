package rt

// Integer is the set of source integer representations.
type Integer interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~int | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uint
}
