// Generated by tmpl
// https://github.com/benbjohnson/tmpl
//
// DO NOT EDIT!
// Source: arrays.gen.go.tmpl

package gen

import (
	"github.com/influxdata/platform/tsdb/cursors"
	"github.com/influxdata/platform/tsdb/tsm1"
)

type FloatArray struct {
	cursors.FloatArray
}

func NewFloatArrayLen(sz int) *FloatArray {
	return &FloatArray{
		FloatArray: cursors.FloatArray{
			Timestamps: make([]int64, sz),
			Values:     make([]float64, sz),
		},
	}
}

func (a *FloatArray) Encode(b []byte) ([]byte, error) {
	return tsm1.EncodeFloatArrayBlock(&a.FloatArray, b)
}

type IntegerArray struct {
	cursors.IntegerArray
}

func NewIntegerArrayLen(sz int) *IntegerArray {
	return &IntegerArray{
		IntegerArray: cursors.IntegerArray{
			Timestamps: make([]int64, sz),
			Values:     make([]int64, sz),
		},
	}
}

func (a *IntegerArray) Encode(b []byte) ([]byte, error) {
	return tsm1.EncodeIntegerArrayBlock(&a.IntegerArray, b)
}

type UnsignedArray struct {
	cursors.UnsignedArray
}

func NewUnsignedArrayLen(sz int) *UnsignedArray {
	return &UnsignedArray{
		UnsignedArray: cursors.UnsignedArray{
			Timestamps: make([]int64, sz),
			Values:     make([]uint64, sz),
		},
	}
}

func (a *UnsignedArray) Encode(b []byte) ([]byte, error) {
	return tsm1.EncodeUnsignedArrayBlock(&a.UnsignedArray, b)
}

type StringArray struct {
	cursors.StringArray
}

func NewStringArrayLen(sz int) *StringArray {
	return &StringArray{
		StringArray: cursors.StringArray{
			Timestamps: make([]int64, sz),
			Values:     make([]string, sz),
		},
	}
}

func (a *StringArray) Encode(b []byte) ([]byte, error) {
	return tsm1.EncodeStringArrayBlock(&a.StringArray, b)
}

type BooleanArray struct {
	cursors.BooleanArray
}

func NewBooleanArrayLen(sz int) *BooleanArray {
	return &BooleanArray{
		BooleanArray: cursors.BooleanArray{
			Timestamps: make([]int64, sz),
			Values:     make([]bool, sz),
		},
	}
}

func (a *BooleanArray) Encode(b []byte) ([]byte, error) {
	return tsm1.EncodeBooleanArrayBlock(&a.BooleanArray, b)
}
