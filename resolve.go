package graphql

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

func GenerateResponse(data string, errors []error) string {
	res := `{"data":` + data
	if len(errors) > 0 {
		res += `"errors":[`
		for i, err := range errors {
			if i > 0 {
				res += ","
			}
			// TODO support locations and path
			// https://spec.graphql.org/June2018/#sec-Errors
			res += fmt.Sprintf(`{"message":%q}`, err.Error())
		}
		res += "]"
	}
	return res + "}"
}

func (s *Schema) Resolve(query string, operatorTarget string) (string, []error) {
	s.m.Lock()
	defer s.m.Unlock()

	fragments, operatorsMap, errs := ParseQueryAndCheckNames(query)
	if len(errs) > 0 {
		return "{}", errs
	}

	ctx := &Ctx{
		fragments:  fragments,
		schema:     s,
		Values:     map[string]interface{}{},
		directvies: []Directives{},
		errors:     []error{},
	}

	switch len(operatorsMap) {
	case 0:
		return "{}", nil
	case 1:
		res := ""
		for _, operator := range operatorsMap {
			res = ctx.start(&operator)
		}
		return res, ctx.errors
	default:
		if operatorTarget == "" {
			return "{}", []error{errors.New("multiple operators without target")}
		}

		operator, ok := operatorsMap[operatorTarget]
		if ok {
			res := ctx.start(&operator)
			return res, ctx.errors
		} else {
			operatorsList := []string{}
			for k := range operatorsMap {
				operatorsList = append(operatorsList, k)
			}
			return "{}", []error{fmt.Errorf("%s is not a valid operator, available operators: %s", operatorTarget, strings.Join(operatorsList, ", "))}
		}
	}
}

func (ctx *Ctx) addErr(err string) {
	ctx.errors = append(ctx.errors, errors.New(err))
}

func (ctx *Ctx) addErrf(err string, args ...interface{}) {
	ctx.errors = append(ctx.errors, fmt.Errorf(err, args...))
}

func (ctx *Ctx) start(operator *Operator) string {
	// TODO add variables to exec ctx
	if operator.directives != nil && len(operator.directives) > 0 {
		ctx.directvies = append(ctx.directvies, operator.directives)
	}

	switch operator.operationType {
	case "query":
		return ctx.resolveSelection(operator.selection, ctx.schema.rootQueryValue, ctx.schema.rootQuery, 0)
	case "mutation":
		return ctx.resolveSelection(operator.selection, ctx.schema.rootQueryValue, ctx.schema.rootMethod, 0)
	case "subscription":
		// TODO
		ctx.addErr("subscription not suppored yet")
		return "{}"
	default:
		ctx.addErrf("%s cannot be used as operator", operator.operationType)
		return "{}"
	}
}

func (ctx *Ctx) resolveSelection(selectionSet SelectionSet, struct_ reflect.Value, structType *Obj, dept uint8) string {
	if dept >= ctx.schema.MaxDepth {
		ctx.addErr("reached max dept")
		return "null"
	}
	dept = dept + 1
	return "{" + ctx.resolveSelectionContent(selectionSet, struct_, structType, dept) + "}"
}

func (ctx *Ctx) resolveSelectionContent(selectionSet SelectionSet, struct_ reflect.Value, structType *Obj, dept uint8) string {
	res := ""
	writtenToRes := false
	for _, selection := range selectionSet {
		switch selection.selectionType {
		case "Field":
			value, hasError := ctx.resolveField(selection.field, struct_, structType, dept)
			if !hasError {
				if writtenToRes {
					res += ","
				} else {
					writtenToRes = true
				}
				res += value
			}
		case "FragmentSpread":
			operator, ok := ctx.fragments[selection.fragmentSpread.name]
			if !ok {
				ctx.addErrf("unknown fragment %s", selection.fragmentSpread.name)
				continue
			}

			value := ctx.resolveSelectionContent(operator.fragment.selection, struct_, structType, dept)
			if len(value) > 0 {
				if writtenToRes {
					res += ","
				} else {
					writtenToRes = true
				}
				res += value
			}
		case "InlineFragment":
			value := ctx.resolveSelectionContent(selection.inlineFragment.selection, struct_, structType, dept)
			if len(value) > 0 {
				if writtenToRes {
					res += ","
				} else {
					writtenToRes = true
				}
				res += value
			}
		}
	}
	return res
}

func (ctx *Ctx) resolveField(query *Field, struct_ reflect.Value, codeStructure *Obj, dept uint8) (fieldValue string, returnedOnError bool) {
	res := func(data string) string {
		name := query.name
		if len(query.alias) > 0 {
			name = query.alias
		}
		return fmt.Sprintf(`"%s":%s`, name, data)
	}

	structItem, ok := codeStructure.objContents[query.name]
	var value reflect.Value
	if !ok {
		ctx.addErrf("field %s does not exists on %s", query.name, codeStructure.typeName)
		return res("null"), true
	}

	if structItem.customObjValue != nil {
		value = *structItem.customObjValue
	} else if structItem.valueType == valueTypeMethod && structItem.method.isTypeMethod {
		value = struct_.MethodByName(structItem.structFieldName)
	} else {
		value = struct_.FieldByName(structItem.structFieldName)
	}

	fieldValue, returnedOnError = ctx.resolveFieldDataValue(query, value, structItem, dept)
	return res(fieldValue), returnedOnError
}

func (ctx *Ctx) matchInputValue(queryValue *Value, goField *reflect.Value, goAnylizedData *Input) error {
	goFieldKind := goAnylizedData.kind

	if goFieldKind == reflect.Ptr {
		if queryValue.isNull {
			// Na mate just keep it at it's default
			return nil
		}

		expectedType := goField.Type().Elem()
		newVal := reflect.New(expectedType)
		newValInner := newVal.Elem()

		err := ctx.matchInputValue(queryValue, &newValInner, goAnylizedData.elem)
		if err != nil {
			return err
		}

		goField.Set(newVal)
		return nil
	}

	mismatchError := func() error {
		m := map[reflect.Kind]string{
			reflect.Invalid:       "an unknown type",
			reflect.Bool:          "a boolean",
			reflect.Int:           "a number",
			reflect.Int8:          "a number",
			reflect.Int16:         "a number",
			reflect.Int32:         "a number",
			reflect.Int64:         "a number",
			reflect.Uint:          "a number",
			reflect.Uint8:         "a number",
			reflect.Uint16:        "a number",
			reflect.Uint32:        "a number",
			reflect.Uint64:        "a number",
			reflect.Uintptr:       "a number",
			reflect.Float32:       "a float",
			reflect.Float64:       "a float",
			reflect.Complex64:     "a number",
			reflect.Complex128:    "a number",
			reflect.Array:         "an array",
			reflect.Chan:          "an unknown type",
			reflect.Func:          "an unknown type",
			reflect.Interface:     "an unknown type",
			reflect.Map:           "an unknown type",
			reflect.Ptr:           "optional type",
			reflect.Slice:         "an array",
			reflect.String:        "a string",
			reflect.Struct:        "a object",
			reflect.UnsafePointer: "a number",
		}

		t := goField.Type()
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		return fmt.Errorf("function arguments type missmatch expected %s", m[t.Kind()])
	}

	if queryValue.isVar {
		// TODO support this
		return errors.New("variable function arguments are currently unsupported")
	} else if queryValue.isNull {
		// Na mate just keep it at it's default
		return nil
	} else if queryValue.isEnum {
		if !goAnylizedData.isEnum {
			return mismatchError()
		}

		enum := definedEnums[goAnylizedData.enumKey]
		value, ok := enum.keyValue[queryValue.enumValue]
		if !ok {
			return fmt.Errorf("unknown enum value %s for enum %s", queryValue.enumValue, goAnylizedData.enumKey)
		}

		switch value.Kind() {
		case reflect.String:
			goField.SetString(value.String())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			goField.SetInt(value.Int())
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			goField.SetUint(value.Uint())
		default:
			return errors.New("internal error, type missmatch on enum")
		}

	} else {
		switch queryValue.valType {
		case reflect.Int:
			switch goFieldKind {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				goField.SetInt(int64(queryValue.intValue))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				goField.SetUint(uint64(queryValue.intValue))
			case reflect.Float32, reflect.Float64:
				goField.SetFloat(float64(queryValue.intValue))
			default:
				return mismatchError()
			}
		case reflect.Float64:
			switch goFieldKind {
			case reflect.Float32, reflect.Float64:
				goField.SetFloat(queryValue.floatValue)
			default:
				return mismatchError()
			}
		case reflect.String:
			if goFieldKind == reflect.String {
				goField.SetString(queryValue.stringValue)
			} else {
				return mismatchError()
			}
		case reflect.Bool:
			if goFieldKind == reflect.Bool {
				goField.SetBool(queryValue.booleanValue)
			} else {
				return mismatchError()
			}
		case reflect.Array:
			if goFieldKind == reflect.Array {
				// TODO
				return errors.New("fixed length arrays not supported")
			} else if goFieldKind == reflect.Slice {
				arr := reflect.MakeSlice(goField.Type(), len(queryValue.listValue), len(queryValue.listValue))

				for i, item := range queryValue.listValue {
					arrayItem := arr.Index(i)
					err := ctx.matchInputValue(&item, &arrayItem, goAnylizedData.elem)
					if err != nil {
						return fmt.Errorf("%s, Array index: [%d]", err.Error(), i)
					}
				}

				goField.Set(arr)
			} else {
				return mismatchError()
			}
		case reflect.Map:
			if goFieldKind == reflect.Struct {
				if goAnylizedData.isStructPointers {
					goAnylizedData = ctx.schema.inTypes[goAnylizedData.structName]
				}

				for queryKey, arg := range queryValue.objectValue {
					structItemMeta, ok := goAnylizedData.structContent[queryKey]
					if !ok {
						return fmt.Errorf("undefined property %s", queryKey)
					}

					field := goField.FieldByName(structItemMeta.goFieldName)
					err := ctx.matchInputValue(&arg, &field, &structItemMeta)
					if err != nil {
						return fmt.Errorf("%s, property: %s", err.Error(), queryKey)
					}
				}
			} else {
				return mismatchError()
			}
		default:
			return errors.New("undefined function input type")
		}
	}

	return nil
}

func (ctx *Ctx) resolveFieldDataValue(query *Field, value reflect.Value, codeStructure *Obj, dept uint8) (fieldValue string, returnedOnError bool) {
	switch codeStructure.valueType {
	case valueTypeMethod:
		if value.IsNil() {
			return "null", false
		}

		method := codeStructure.method

		inputs := []reflect.Value{}
		for _, in := range method.ins {
			if in.isCtx {
				inputs = append(inputs, reflect.ValueOf(ctx))
			} else {
				inputs = append(inputs, reflect.New(*in.type_).Elem())
			}
		}

		for queryKey, queryValue := range query.arguments {
			inField, ok := method.inFields[queryKey]
			if !ok {
				ctx.addErrf("undefined function %s input: %s", query.name, queryKey)
				continue
			}
			goField := inputs[inField.inputIdx].FieldByName(inField.input.goFieldName)

			err := ctx.matchInputValue(&queryValue, &goField, &inField.input)
			if err != nil {
				ctx.addErrf("%s, function: %s, property: %s", err.Error(), query.name, queryKey)
				return "null", true
			}
		}

		outs := value.Call(inputs)

		if method.errorOutNr != nil {
			errOut := outs[*method.errorOutNr]
			if !errOut.IsNil() {
				err, ok := errOut.Interface().(error)
				if !ok {
					ctx.addErrf("field %s returned a invalid kind of error", query.name)
					return "null", true
				} else if err != nil {
					ctx.addErr(err.Error())
				}
			}
		}

		return ctx.resolveFieldDataValue(query, outs[method.outNr], &method.outType, dept)
	case valueTypeArray:
		if (value.Kind() != reflect.Array && value.Kind() != reflect.Slice) || value.IsNil() {
			return "null", false
		}

		if codeStructure.innerContent == nil {
			ctx.addErrf("field %s does not have an internal type of an array", query.name)
			return "null", true
		}
		codeStructure = codeStructure.innerContent

		list := []string{}
		for i := 0; i < value.Len(); i++ {
			res, _ := ctx.resolveFieldDataValue(query, value.Index(i), codeStructure, dept)
			list = append(list, res)
		}
		return fmt.Sprintf("[%s]", strings.Join(list, ",")), false
	case valueTypeObj, valueTypeObjRef:
		if len(query.selection) == 0 {
			ctx.addErrf("field %s must have a selection", query.name)
			return "null", true
		}

		var ok bool
		if codeStructure.valueType == valueTypeObjRef {
			codeStructure, ok = ctx.schema.types[codeStructure.typeName]
			if !ok {
				ctx.addErrf("field %s cannot have a selection", query.name)
				return "null", true
			}
		}

		val := ctx.resolveSelection(query.selection, value, codeStructure, dept)
		return val, false
	case valueTypeData:
		if len(query.selection) > 0 {
			ctx.addErrf("field %s cannot have a selection", query.name)
			return "null", true
		}
		val, _ := valueToJson(value.Interface())
		return val, false
	case valueTypePtr:
		if value.Kind() != reflect.Ptr || value.IsNil() {
			return "null", false
		}

		return ctx.resolveFieldDataValue(query, value.Elem(), codeStructure.innerContent, dept)
	case valueTypeEnum:
		enum := definedEnums[codeStructure.enumKey]

		key := enum.valueKey.MapIndex(value)
		if !key.IsValid() {
			return "null", false
		}
		return `"` + key.Interface().(string) + `"`, false
	default:
		ctx.addErrf("field %s has invalid data type", query.name)
		return "null", true
	}
}

func valueToJson(in interface{}) (string, error) {
	switch v := in.(type) {
	case string:
		return fmt.Sprintf("%q", v), nil
	case bool:
		if v {
			return "true", nil
		} else {
			return "false", nil
		}
	case int:
		return fmt.Sprintf("%d", v), nil
	case int8:
		return fmt.Sprintf("%d", v), nil
	case int16:
		return fmt.Sprintf("%d", v), nil
	case int32: // == rune
		return fmt.Sprintf("%d", v), nil
	case int64:
		return fmt.Sprintf("%d", v), nil
	case uint:
		return fmt.Sprintf("%d", v), nil
	case uint8: // == byte
		return fmt.Sprintf("%d", v), nil
	case uint16:
		return fmt.Sprintf("%d", v), nil
	case uint32:
		return fmt.Sprintf("%d", v), nil
	case uint64:
		return fmt.Sprintf("%d", v), nil
	case uintptr:
		return fmt.Sprintf("%d", v), nil
	case float32:
		return floatToJson(32, float64(v)), nil
	case float64:
		return floatToJson(64, v), nil
	case *string:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *bool:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *int:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *int8:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *int16:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *int32: // = *rune
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *int64:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uint:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uint8: // = *byte
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uint16:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uint32:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uint64:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *uintptr:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *float32:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	case *float64:
		if v == nil {
			return "null", nil
		}
		return valueToJson(*v)
	default:
		return "null", errors.New("invalid data type")
	}
}
