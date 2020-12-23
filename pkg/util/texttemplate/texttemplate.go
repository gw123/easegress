package texttemplate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/valyala/fasttemplate"
)

// The complete fomat of template sentence  is
// ${beginToken}${tag1}${seperator}${tag2}${seperator}...${endtoken}
// e.g., if beginToken is '[[', endtoken is ']]', seperator is '.'
// [[plugin.{}.req.body.{gjson}]]
// [[plugin.{}.req.body.{gjson}]]
// TemplateEngine is the abstract implementer
// Template is the part of user's input string's which we want the TempalteEngine to render it
// MetaTemplate is the description collections for TemplateEngine to identify user's valid template rules
const (
	// regexpSyntax = "\\[\\[(.*?)\\]\\]"

	// WidecardTag means accepting any none empty string
	// if chose "{}" to accept any none empty string, then should provide another tag value at that level
	WidecardTag = "{}"

	// GJSONTag is teh special hardcode tag for indicating GJSON syntax, must appear in the last
	// of one template, if chose "{GJSON}", should provid antoher tag value at that level
	GJSONTag = "{gjson}"

	DefulatBeginToken = "[["
	DefulatEndToken   = "]]"
	DefaultSepertor   = "."
)

type node struct {
	Value    string // The tag,e.g. 'plugin', 'req'
	Children []*node
}

// TemplateEngine is the basic API collection for a tempalte usage
type TemplateEngine interface {
	// Rendering e.g., [[xxx.xx.dd.xx]]'s value is 'value0', [[yyy.www.zzz]]'s value is 'value1'
	// "aaa-[[xxx.xx.dd.xx]]-bbb 10101-[[yyy.wwww.zzz]]-9292" will be rendered to "aaa-value0-bbb 10101-value1-9292"
	// Also support GJSON syntax at last tag
	Render(input string) (string, error)

	// ExtractTemplateRuleMap extracts templates from input string
	// return map's key is the template, the value is the matched and rendered metaTemplate
	ExtractTemplateRuleMap(input string) map[string]string

	// ExtractTemplateRuleMap extracts templates from input string
	// return map's key is the template, the value is the matched and rendered metaTemplate or empty
	ExtractRawTemplateRuleMap(input string) map[string]string

	// HasTemplates checks whether has templates in input string or not
	HasTemplates(input string) bool

	// MatchMetaTemplate return original template or replace with {gjson} at last tag, "" if not metaTemplate matched
	MatchMetaTemplate(template string) string

	// SetDict adds a temaplateRule and its value for later rendering
	SetDict(template string, value interface{}) error

	// GetDict returns the template rely dictionary
	GetDict() map[string]interface{}
}

// DummyTemplate return a empty implement
type DummyTemplate struct {
}

// Render dummy implement
func (DummyTemplate) Render(input string) (string, error) {
	return "", nil
}

// ExtractTemplateRuleMap dummy implement
func (DummyTemplate) ExtractTemplateRuleMap(input string) map[string]string {
	m := make(map[string]string, 0)
	return m
}

// ExtractTemplateRuleMap dummy implement
func (DummyTemplate) ExtractRawTemplateRuleMap(input string) map[string]string {
	m := make(map[string]string, 0)
	return m
}

// SetDict the dummy implement
func (DummyTemplate) SetDict(template string, value interface{}) error {
	return nil
}

// MatchMetaTemplate dummy implement
func (DummyTemplate) MatchMetaTemplate(template string) string {
	return ""
}

// GetDict the dummy implement
func (DummyTemplate) GetDict() map[string]interface{} {
	m := make(map[string]interface{}, 0)
	return m
}

// HasTemplates the dummy implement
func (DummyTemplate) HasTemplates(input string) bool {
	return false
}

// TextTemplate wraps a fasttempalte rendering and a
// template syntax tree for validation, the valid tempalte and its
// value can be added into dictionary for rendering
type TextTemplate struct {
	ft         *fasttemplate.Template
	beginToken string
	endToken   string
	seperator  string

	metaTemplates []string               // the user raw input candidate templates
	root          *node                  // The template syntax tree root node generated by use's input raw templates
	dict          map[string]interface{} // using `interface{}` for fasttemplate's API
}

// NewDefault returns Tempalte interface implementer with default config and customize meatTemplates
func NewDefault(metaTemplates []string) (TemplateEngine, error) {
	t := TextTemplate{
		beginToken:    DefulatBeginToken,
		endToken:      DefulatEndToken,
		seperator:     DefaultSepertor,
		metaTemplates: metaTemplates,
		dict:          map[string]interface{}{},
	}

	if err := t.buildTemplateTree(); err != nil {
		return DummyTemplate{}, err
	}

	return t, nil

}

// New returns a new Tempalte interface implementer, return a dummy template if something wrong,
// and in that case, the didicated reason will set into error return
func New(beginToken, endToken, seperator string, metaTemplates []string) (TemplateEngine, error) {
	if len(beginToken) == 0 || len(endToken) == 0 || len(seperator) == 0 || len(metaTemplates) == 0 {
		return DummyTemplate{}, fmt.Errorf("invalid input, beingToken %s, endToken %s, seperator = %s , metaTempaltes %v",
			beginToken, endToken, seperator, metaTemplates)
	}
	t := &TextTemplate{
		beginToken:    beginToken,
		endToken:      endToken,
		seperator:     seperator,
		metaTemplates: metaTemplates,
		dict:          map[string]interface{}{},
	}

	if err := t.buildTemplateTree(); err != nil {
		return DummyTemplate{}, err
	}

	return t, nil
}

// NewDummyTemplate returns a dummy template implement
func NewDummyTemplate() TemplateEngine {
	return DummyTemplate{}
}

// GetDict return the dictionary of texttemplate
func (t TextTemplate) GetDict() map[string]interface{} {
	return t.dict
}

func (t *TextTemplate) indexChild(children []*node, target string) int {
	for i, v := range children {
		if target == v.Value {
			return i
		}
	}
	return -1
}

func (t *TextTemplate) addNode(tags []string) {

	if t.root == nil {
		t.root = &node{}
	}

	parent := t.root
	for _, v := range tags {
		index := t.indexChild(parent.Children, v)

		if index != -1 {
			parent = parent.Children[index]
			continue
		} else {
			tmp := &node{
				Value: v,
			}
			parent.Children = append(parent.Children, tmp)
			parent = tmp
		}
	}

	return
}

func (t *TextTemplate) validateTree(root *node) error {
	if len(root.Children) == 0 {
		return nil
	}

	if index := t.indexChild(root.Children, WidecardTag); index != -1 {
		if len(root.Children) != 1 {
			return fmt.Errorf("{} wildcard and other tags exist at the same level")
		}
	}

	if index := t.indexChild(root.Children, GJSONTag); index != -1 {
		if len(root.Children) != 1 {
			return fmt.Errorf("{gjson} GJSON and other tags exist at the same level")
		}
	}

	for i := 0; i < len(root.Children); i++ {
		if err := t.validateTree(root.Children[i]); err != nil {
			return err
		}
	}

	return nil
}

//
func (t *TextTemplate) buildTemplateTree() error {
	if len(t.metaTemplates) == 0 {
		return fmt.Errorf("empty templates")
	}

	for _, v := range t.metaTemplates {
		arr := strings.Split(v, t.seperator)
		if len(arr) == 0 {
			return fmt.Errorf("invalid tempalte %s by seperator %s",
				v, t.seperator)
		}

		for i, tag := range arr {
			if len(tag) == 0 {
				return fmt.Errorf("invalid empty tag, template %s index %d seprator %s",
					v, i, t.seperator)
			}

			if tag == GJSONTag && i != len(arr)-1 {
				return fmt.Errorf("invalid %s: GJSON tag should only appear at the ending if need",
					v)
			}
		}
	}
	// every singl template is valid
	for _, v := range t.metaTemplates {
		arr := strings.Split(v, t.seperator)
		t.addNode(arr)
	}

	// validete the whole template tree
	if err := t.validateTree(t.root); err != nil {
		t.root = nil
		return fmt.Errorf("invalid templates %v, err is %v ", t.metaTemplates, err)
	}
	return nil
}

// MatchMetaTemplate travels the metaTemplate syntax tree and return the frist match template
// if matched found
//   e.g. template is "plugin.abc.req.body.friends.#(last=="Murphy").first" match "plugin.{}.req.body.{gjson}"
//   	will return "plugin.abc.req.body.{gjson}"
//   e.g. template is "pluign.abc.req.body" match "plugin.{}.req.body"
//   	will return "plugin.abc.req.body"
// if not any template matched found, then return ""
func (t TextTemplate) MatchMetaTemplate(template string) string {
	tags := strings.Split(template, t.seperator)
	if len(tags) == 0 {
		return ""
	}

	root := t.root
	index := 0
	hasGJSON := false

	for ; index < len(tags); index++ {
		// no tag remain to match, or its a empty tag
		if len(root.Children) == 0 || len(tags[index]) == 0 {
			return ""
		}

		if len(root.Children) == 1 {
			if root.Children[0].Value == GJSONTag {
				hasGJSON = true
				break
			}
			if root.Children[0].Value == WidecardTag || root.Children[0].Value == tags[index] {
				root = root.Children[0]
				continue
			} else {
				return ""
			}
		} else {
			if index := t.indexChild(root.Children, tags[index]); index != -1 {
				root = root.Children[index]
			} else {
				// no match at current level, return fail directly
				return ""
			}
		}
	}

	if hasGJSON {
		// replace left gjson syntax with GJSONTag
		return strings.Join(tags[:index], t.seperator) + t.seperator + GJSONTag
	}

	return template
}

func (t TextTemplate) extractVarsAroundToken(input string) []string {
	arr := []string{}
	for len(input) != 0 {
		bIdx := strings.Index(input, t.beginToken)
		if bIdx == -1 {
			break
		}

		input = input[bIdx+len(t.beginToken):] // jump over the begin token
		eIdx := strings.Index(input, t.endToken)

		if eIdx == -1 {
			break
		}

		arr = append(arr, input[:eIdx])
		input = input[eIdx:]
	}

	return arr
}

// ExtractTemplateRuleMap extracts candidate templates from input string
// return map's key is the candidate template, the value is the matched template
func (t TextTemplate) ExtractTemplateRuleMap(input string) map[string]string {
	results := t.extractVarsAroundToken(input)
	m := map[string]string{}

	for _, v := range results {
		metaTemplate := t.MatchMetaTemplate(v)

		if len(metaTemplate) != 0 {
			m[v] = metaTemplate
		}
	}

	return m
}

// ExtractRawTemplateRuleMap extracts all candidate templates (valid/invalid)
// from input string
func (t TextTemplate) ExtractRawTemplateRuleMap(input string) map[string]string {
	results := t.extractVarsAroundToken(input)
	m := map[string]string{}

	for _, v := range results {
		metaTemplate := t.MatchMetaTemplate(v)

		if len(metaTemplate) != 0 {
			m[v] = metaTemplate
		} else {
			m[v] = ""
		}
	}

	return m
}

// SetDict adds a temaplateRule into dictionary if it contains any templates.
func (t TextTemplate) SetDict(template string, value interface{}) error {
	if tmp := t.MatchMetaTemplate(template); len(tmp) != 0 {
		t.dict[template] = value
		return nil
	}

	return fmt.Errorf("matched none template , input %s ", template)
}

func (t *TextTemplate) setWithGJSON(template, metaTemplate string) error {
	keyIndict := strings.TrimRight(metaTemplate, t.seperator+GJSONTag)
	gjsonSyntax := strings.TrimPrefix(template, keyIndict+t.seperator)

	if valueForGJSON, exist := t.dict[keyIndict]; exist {
		if err := t.SetDict(template, gjson.Get(valueForGJSON.(string), gjsonSyntax).String()); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("set gjson found no syntax target, tempalte %s", template)
	}

	return nil
}

// HasTemplates check a string contain any valid templates
func (t TextTemplate) HasTemplates(input string) bool {
	if len(t.ExtractTemplateRuleMap(input)) == 0 {
		return false
	}

	return true
}

// Render uses a fasttemplate and dictionary to rendering
//  e.g., [[xxx.xx.dd.xx]]'s value in dictionary is 'value0', [[yyy.www.zzz]]'s value is 'value1'
// "aaa-[[xxx.xx.dd.xx]]-bbb 10101-[[yyy.wwww.zzz]]-9292" will be rendered to "aaa-value0-bbb 10101-value1-9292"
// if containers any new GJSON syntax, it will use 'gjson.Get' to extract result then store into dictionary before
// rendering
func (t TextTemplate) Render(input string) (string, error) {
	templateMap := t.ExtractTemplateRuleMap(input)

	// find no template to render
	if len(templateMap) == 0 {
		return input, nil
	}

	for k, v := range templateMap {
		// has new gjson syntax, add manually
		if strings.Contains(v, GJSONTag) {
			if _, exist := t.dict[k]; !exist {
				if err := t.setWithGJSON(k, v); err != nil {
					return "", err
				}
			}
		}
	}

	t.ft = fasttemplate.New(input, t.beginToken, t.endToken)
	return t.ft.ExecuteString(t.dict), nil
}
