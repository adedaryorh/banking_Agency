package repository

import (
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

var (
	textType   = reflect.TypeOf(pgtype.Text{})
	float8Type = reflect.TypeOf(pgtype.Float8{})
)

func copyFields(destination, source any) {
	dst := reflect.ValueOf(destination)
	if dst.Kind() != reflect.Pointer || dst.IsNil() {
		return
	}
	dst = dst.Elem()
	src := reflect.ValueOf(source)
	if src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return
		}
		src = src.Elem()
	}
	if dst.Kind() != reflect.Struct || src.Kind() != reflect.Struct {
		return
	}
	fields := make(map[string]reflect.Value, src.NumField())
	for i := 0; i < src.NumField(); i++ {
		fields[normalField(src.Type().Field(i).Name)] = src.Field(i)
	}
	for i := 0; i < dst.NumField(); i++ {
		field := dst.Field(i)
		if !field.CanSet() {
			continue
		}
		value, ok := fields[normalField(dst.Type().Field(i).Name)]
		if !ok {
			continue
		}
		assignValue(field, value)
	}
}

func normalField(value string) string { return strings.ToLower(value) }

func assignValue(dst, src reflect.Value) {
	if src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return
		}
		if dst.Type() == float8Type && src.Type().Elem().Kind() == reflect.Float64 {
			dst.Set(reflect.ValueOf(pgtype.Float8{Float64: src.Elem().Float(), Valid: true}))
			return
		}
		if dst.Kind() == reflect.Pointer && src.Type().AssignableTo(dst.Type()) {
			dst.Set(src)
			return
		}
		src = src.Elem()
	}
	if dst.Type() == textType {
		if src.Kind() == reflect.String {
			value := src.String()
			dst.Set(reflect.ValueOf(pgtype.Text{String: value, Valid: value != ""}))
		}
		return
	}
	if src.Type() == textType {
		value := src.Interface().(pgtype.Text)
		if !value.Valid {
			return
		}
		if dst.Kind() == reflect.String {
			dst.SetString(value.String)
		} else if dst.Kind() == reflect.Pointer && dst.Type().Elem().Kind() == reflect.String {
			text := value.String
			dst.Set(reflect.ValueOf(&text))
		}
		return
	}
	if dst.Type() == float8Type && src.Kind() == reflect.Float64 {
		dst.Set(reflect.ValueOf(pgtype.Float8{Float64: src.Float(), Valid: true}))
		return
	}
	if src.Type() == float8Type && dst.Kind() == reflect.Float64 {
		value := src.Interface().(pgtype.Float8)
		if value.Valid {
			dst.SetFloat(value.Float64)
		}
		return
	}
	if src.Type() == float8Type && dst.Kind() == reflect.Pointer && dst.Type().Elem().Kind() == reflect.Float64 {
		value := src.Interface().(pgtype.Float8)
		if value.Valid {
			number := value.Float64
			dst.Set(reflect.ValueOf(&number))
		}
		return
	}
	if src.Type().AssignableTo(dst.Type()) {
		dst.Set(src)
		return
	}
	if src.Type().ConvertibleTo(dst.Type()) {
		dst.Set(src.Convert(dst.Type()))
		return
	}
	if dst.Kind() == reflect.Pointer && src.Type().AssignableTo(dst.Type().Elem()) {
		value := reflect.New(dst.Type().Elem())
		value.Elem().Set(src)
		dst.Set(value)
	}
}

func mapped[T any](source any) T {
	var destination T
	copyFields(&destination, source)
	return destination
}
