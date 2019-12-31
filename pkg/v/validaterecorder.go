package v

import (
	"fmt"
	"log"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/megaease/easegateway/pkg/logger"

	loadjs "github.com/xeipuuv/gojsonschema"
	yaml "gopkg.in/yaml.v2"
)

type (

	// FormatFunc validates the customized format in json schema.
	// The function could panic if the types are unexpected.
	FormatFunc func(v interface{}) error

	// Validator stands for the types which needs its own Validate function.
	Validator interface {
		Validate() error
	}

	// ValidateRecorder records varied errors after validating.
	ValidateRecorder struct {
		// JSONSchemaErrs generated by vendor json schema.
		JSONSchemaErrs []string `yaml:"jsonschemaErrs,omitempty"`
		// FormatErrs generated by the format function of the single field.
		FormatErrs []string `yaml:"formatErrs,omitempty"`
		// GeneralErrs generated by Validate() of the Validator itself.
		GeneralErrs []string `yaml:"generalErrs,omitempty"`

		// SystemErr stands internal error, which often means bugs.
		SystemErr string `yaml:"systemErr,omitempty"`
	}
)

func (vr *ValidateRecorder) recordJSONSchema(result *loadjs.Result) {
	for _, err := range result.Errors() {
		vr.JSONSchemaErrs = append(vr.JSONSchemaErrs, err.String())
	}
}

func getFieldYAMLName(field *reflect.StructField) string {
	fieldName := field.Name

	fieldNames := strings.Split(field.Tag.Get("yaml"), ",")
	if len(fieldNames) > 0 {
		fieldName = fieldNames[0]
	}

	return fieldName
}

func requiredFromField(field *reflect.StructField) bool {
	tags := strings.Split(field.Tag.Get("jsonschema"), ",")
	switch {
	case len(tags) == 0:
		return false
	case tags[0] == "-":
		return false
	default:
		for _, tag := range tags {
			if tag == "omitempty" {
				return false
			}
		}
		// NOTICE: Required by default.
		return true
	}
}

func (vr *ValidateRecorder) recordFormat(val *reflect.Value, field *reflect.StructField) {
	if field == nil {
		return
	}

	if !requiredFromField(field) && val.IsZero() {
		return
	}

	tags := strings.Split(field.Tag.Get("jsonschema"), ",")
	for _, tag := range tags {
		nameValue := strings.Split(tag, "=")
		if len(nameValue) != 2 {
			continue
		}

		name, value := nameValue[0], nameValue[1]
		if name != "format" {
			continue
		}

		fn, ok := getFormatFunc(value)
		if !ok {
			logger.Errorf("BUG: format function %s not found", value)
			return
		}

		err := fn(val.Interface())
		if err != nil {
			vr.FormatErrs = append(vr.FormatErrs,
				fmt.Sprintf("%s: %s",
					getFieldYAMLName(field),
					err.Error()))
		}
	}
}

func (vr *ValidateRecorder) recordGeneral(val *reflect.Value, field *reflect.StructField) {
	fieldName := val.Type().String()
	if field != nil {
		fieldName = getFieldYAMLName(field)
	}

	v, ok := val.Interface().(Validator)

	if !ok {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("BUG: call Validate for %T panic: %v", v, r)
			logger.Errorf("%v: %s", err, debug.Stack())
			vr.recordSystem(err)
		}
	}()

	err := v.Validate()
	if err != nil {
		vr.GeneralErrs = append(vr.GeneralErrs, fmt.Sprintf("%s: %s",
			fieldName,
			err.Error()))
	}
}

func (vr *ValidateRecorder) recordSystem(err error) {
	if err != nil {
		vr.SystemErr = err.Error()
	}
}

func (vr *ValidateRecorder) Error() string {
	return vr.String()
}

func (vr *ValidateRecorder) String() string {
	buff, err := yaml.Marshal(vr)
	if err != nil {
		log.Printf("BUG: marshal %#v to yaml failed: %v", vr, err)
	}
	return string(buff)
}

// Valid represents if the result is valid.
func (vr *ValidateRecorder) Valid() bool {
	return len(vr.JSONSchemaErrs) == 0 && len(vr.FormatErrs) == 0 &&
		len(vr.GeneralErrs) == 0 && len(vr.SystemErr) == 0
}
