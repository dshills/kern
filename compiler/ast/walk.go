package ast

import "reflect"

// Walk visits every node reachable from n (pointers to structs, interfaces
// and slices) depth first; visit returns true to stop. It is reflective, so
// it covers every node type without a per-type case.
func Walk(n any, visit func(any) bool) bool {
	return walkValue(reflect.ValueOf(n), visit, map[uintptr]bool{})
}

func walkValue(v reflect.Value, visit func(any) bool, seen map[uintptr]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return false
		}
		if v.Elem().Kind() == reflect.Struct {
			if seen[v.Pointer()] {
				return false
			}
			seen[v.Pointer()] = true
			if visit(v.Interface()) {
				return true
			}
		}
		return walkValue(v.Elem(), visit, seen)
	case reflect.Interface:
		if v.IsNil() {
			return false
		}
		return walkValue(v.Elem(), visit, seen)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && walkValue(v.Field(i), visit, seen) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if walkValue(v.Index(i), visit, seen) {
				return true
			}
		}
	}
	return false
}
