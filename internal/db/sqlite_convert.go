package db

import (
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
)

func init() {
	// now() da la hora como se guardan las fechas; lower, con Unicode como
	// el de Postgres (el de SQLite solo cambia ASCII).
	sqlite.MustRegisterScalarFunction("now", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		return time.Now().UnixMicro(), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return strings.ToLower(v), nil
		case []byte:
			return strings.ToLower(string(v)), nil
		}
		return args[0], nil
	})
}

var (
	timeType    = reflect.TypeFor[time.Time]()
	rawJSONType = reflect.TypeFor[json.RawMessage]()
	scannerType = reflect.TypeFor[sql.Scanner]()
)

// sqliteArgs pasa los argumentos a lo que guarda SQLite: time.Time a
// microsegundos, los slices (salvo []byte), structs y mapas a texto JSON.
func sqliteArgs(args []any) ([]any, error) {
	out := make([]any, len(args))
	for i, a := range args {
		v, err := sqliteValue(a)
		if err != nil {
			return nil, fmt.Errorf("argumento $%d: %w", i+1, err)
		}
		out[i] = v
	}
	return out, nil
}

func sqliteValue(a any) (any, error) {
	switch v := a.(type) {
	case nil:
		return nil, nil
	case time.Time:
		return v.UnixMicro(), nil
	case json.RawMessage:
		if v == nil {
			return nil, nil
		}
		return string(v), nil
	case []byte:
		if v == nil {
			return nil, nil
		}
		return v, nil
	case string, int64, float64, bool:
		return v, nil
	case driver.Valuer:
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Pointer && rv.IsNil() {
			return nil, nil
		}
		dv, err := v.Value()
		if err != nil {
			return nil, err
		}
		return sqliteValue(dv)
	}
	rv := reflect.ValueOf(a)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, nil
		}
		return sqliteValue(rv.Elem().Interface())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return nil, fmt.Errorf("%d doesn't fit in an SQLite integer", u)
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.String:
		return rv.String(), nil
	case reflect.Slice:
		if rv.IsNil() {
			return nil, nil
		}
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return rv.Bytes(), nil
		}
		return jsonList(rv)
	case reflect.Array:
		return jsonList(rv)
	case reflect.Map, reflect.Struct:
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	}
	return nil, fmt.Errorf("type %T has no conversion to SQLite", a)
}

// jsonList es una lista como array JSON, con los elementos como los guarda
// SQLite: las fechas en microsegundos y los []byte en hexadecimal.
func jsonList(rv reflect.Value) (any, error) {
	items := make([]any, rv.Len())
	for i := range items {
		e := rv.Index(i)
		for e.Kind() == reflect.Pointer || e.Kind() == reflect.Interface {
			if e.IsNil() {
				break
			}
			e = e.Elem()
		}
		switch {
		case (e.Kind() == reflect.Pointer || e.Kind() == reflect.Interface) && e.IsNil():
			items[i] = nil
		case e.Type() == timeType:
			items[i] = e.Interface().(time.Time).UnixMicro()
		case e.Kind() == reflect.Slice && e.Type().Elem().Kind() == reflect.Uint8 && e.Type() != rawJSONType:
			if e.IsNil() {
				items[i] = nil
			} else {
				items[i] = hex.EncodeToString(e.Bytes())
			}
		default:
			items[i] = e.Interface()
		}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// scanTargets envuelve cada destino para leer lo que da SQLite.
func scanTargets(dest []any) []any {
	out := make([]any, len(dest))
	for i, d := range dest {
		out[i] = &sqliteScanner{dest: d}
	}
	return out
}

type sqliteScanner struct{ dest any }

func (s *sqliteScanner) Scan(src any) error {
	if b, ok := src.([]byte); ok {
		src = append([]byte(nil), b...)
	}
	return assign(s.dest, src)
}

// assign deja src, un valor de SQLite, en dest, un puntero.
func assign(dest, src any) error {
	switch d := dest.(type) {
	case *any:
		*d = src
		return nil
	case sql.Scanner:
		return d.Scan(src)
	}
	dv := reflect.ValueOf(dest)
	if dv.Kind() != reflect.Pointer || dv.IsNil() {
		return fmt.Errorf("destination %T is not a pointer", dest)
	}
	return assignValue(dv.Elem(), src)
}

func assignValue(v reflect.Value, src any) error {
	t := v.Type()
	if reflect.PointerTo(t).Implements(scannerType) {
		return v.Addr().Interface().(sql.Scanner).Scan(src)
	}
	if t.Kind() == reflect.Pointer {
		if src == nil {
			v.SetZero()
			return nil
		}
		n := reflect.New(t.Elem())
		if err := assignValue(n.Elem(), src); err != nil {
			return err
		}
		v.Set(n)
		return nil
	}
	if t == timeType {
		tm, err := toTime(src)
		if err != nil {
			return err
		}
		v.Set(reflect.ValueOf(tm))
		return nil
	}
	if t.Kind() == reflect.Interface {
		if src == nil {
			v.SetZero()
		} else {
			v.Set(reflect.ValueOf(src))
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Struct, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			switch s := src.(type) {
			case nil:
				v.SetZero()
			case []byte:
				v.SetBytes(s)
			case string:
				v.SetBytes([]byte(s))
			default:
				return fmt.Errorf("can't read %T into %s", src, t)
			}
			return nil
		}
		var b []byte
		switch s := src.(type) {
		case nil:
			v.SetZero()
			return nil
		case string:
			b = []byte(s)
		case []byte:
			b = s
		default:
			return fmt.Errorf("can't read %T into %s", src, t)
		}
		n := reflect.New(t)
		if err := json.Unmarshal(b, n.Interface()); err != nil {
			return fmt.Errorf("read JSON into %s: %w", t, err)
		}
		v.Set(n.Elem())
		return nil
	}
	if src == nil {
		return fmt.Errorf("can't read NULL into %s", t)
	}
	switch t.Kind() {
	case reflect.String:
		switch s := src.(type) {
		case string:
			v.SetString(s)
		case []byte:
			v.SetString(string(s))
		case int64:
			v.SetString(strconv.FormatInt(s, 10))
		case float64:
			v.SetString(strconv.FormatFloat(s, 'g', -1, 64))
		default:
			return fmt.Errorf("can't read %T into %s", src, t)
		}
	case reflect.Bool:
		switch s := src.(type) {
		case int64:
			v.SetBool(s != 0)
		case float64:
			v.SetBool(s != 0)
		case bool:
			v.SetBool(s)
		case string:
			b, err := strconv.ParseBool(s)
			if err != nil {
				return err
			}
			v.SetBool(b)
		default:
			return fmt.Errorf("can't read %T into %s", src, t)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt(src)
		if err != nil {
			return err
		}
		if v.OverflowInt(n) {
			return fmt.Errorf("%d doesn't fit in %s", n, t)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := toInt(src)
		if err != nil {
			return err
		}
		if n < 0 || v.OverflowUint(uint64(n)) {
			return fmt.Errorf("%d doesn't fit in %s", n, t)
		}
		v.SetUint(uint64(n))
	case reflect.Float32, reflect.Float64:
		switch s := src.(type) {
		case int64:
			v.SetFloat(float64(s))
		case float64:
			v.SetFloat(s)
		case string:
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return err
			}
			v.SetFloat(f)
		default:
			return fmt.Errorf("can't read %T into %s", src, t)
		}
	default:
		return fmt.Errorf("can't read %T into %s", src, t)
	}
	return nil
}

func toInt(src any) (int64, error) {
	switch s := src.(type) {
	case int64:
		return s, nil
	case float64:
		return int64(math.Round(s)), nil
	case bool:
		if s {
			return 1, nil
		}
		return 0, nil
	case string:
		return strconv.ParseInt(s, 10, 64)
	case []byte:
		return strconv.ParseInt(string(s), 10, 64)
	}
	return 0, fmt.Errorf("can't read %T as an integer", src)
}

// toTime lee una fecha: microsegundos desde 1970, o un día AAAA-MM-DD.
func toTime(src any) (time.Time, error) {
	switch s := src.(type) {
	case int64:
		return time.UnixMicro(s), nil
	case float64:
		return time.UnixMicro(int64(math.Round(s))), nil
	case time.Time:
		return s, nil
	case []byte:
		return toTime(string(s))
	case string:
		if t, err := time.Parse(time.DateOnly, s); err == nil {
			return t, nil
		}
		return time.Parse(time.RFC3339Nano, s)
	case nil:
		return time.Time{}, fmt.Errorf("can't read NULL into time.Time")
	}
	return time.Time{}, fmt.Errorf("can't read %T as a time", src)
}
