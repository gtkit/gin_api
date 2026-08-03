package openapi

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// 本文件是计划要求的**选型实测**：逐项跑最小样例验证 7 类难点。
//
// 判定标准（来自计划）：7 类的覆盖度，以及是否需要在 dto 上加侵入式 tag。
// 每个用例的注释写明该类难点"猜错会怎样"，因为那正是选型的依据。

// --- 1. validation tag → schema 约束 ---

type tagged struct {
	// 字符串的 min/max 是**长度**。
	Name string `json:"name" binding:"required,min=2,max=50"`
	// 数值的 min/max 是**取值范围**。同一对 tag，两种完全不同的含义。
	Age   int    `json:"age" binding:"omitempty,min=0,max=150"`
	Email string `json:"email" binding:"omitempty,email"`
	Site  string `json:"site" binding:"omitempty,url"`
}

// TestValidationTagsMapToConstraints 验证 min/max 按 schema 类型分流。
//
// 搞反的话，一个 `max=50` 的昵称会被声明成"数值不超过 50"——
// 客户端据此生成的校验完全不起作用，而文档看起来是有约束的。
func TestValidationTagsMapToConstraints(t *testing.T) {
	t.Parallel()

	props := mustProperties(t, tagged{})

	name := props["name"]
	if name.MaxLength == nil || *name.MaxLength != 50 {
		t.Fatalf("name maxLength = %v, want 50 (string min/max is a length)", name.MaxLength)
	}
	if name.Maximum != nil {
		t.Fatal("name must not carry a numeric maximum")
	}

	age := props["age"]
	if age.Maximum == nil || *age.Maximum != 150 {
		t.Fatalf("age maximum = %v, want 150 (numeric min/max is a range)", age.Maximum)
	}
	if age.MaxLength != nil {
		t.Fatal("age must not carry a string maxLength")
	}

	if props["email"].Format != "email" {
		t.Fatalf("email format = %q", props["email"].Format)
	}
	if props["site"].Format != "uri" {
		t.Fatalf("site format = %q", props["site"].Format)
	}
}

// --- 2. 指针的 required 与 nullable 二义 ---

type pointerCases struct {
	// 必填且可为 null：两种语义独立成立。
	RequiredNullable *int8 `json:"required_nullable" binding:"required"`
	// 可选且可为 null。
	OptionalNullable *int8 `json:"optional_nullable" binding:"omitempty"`
	// 必填且非 null。
	RequiredValue string `json:"required_value" binding:"required"`
	// 可选且非 null（json omitempty）。
	OptionalValue string `json:"optional_value,omitempty"`
}

// TestPointerSemanticsAreSeparated 钉住 Go 的 *T 两种语义的区分。
//
// Go 的 *T 同时承载"可以不传"与"可以是 null"。把它们混为一谈会让契约
// 要么过严（拒绝合法请求）、要么过松（漏掉必填校验）——而两种错误
// 都只在真实客户端接入时才暴露。
func TestPointerSemanticsAreSeparated(t *testing.T) {
	t.Parallel()

	schema := mustSchema(t, pointerCases{})
	props := schema.Properties

	// 可空性来自指针本身。
	if !isNullable(props["required_nullable"]) {
		t.Fatal("a pointer field must be nullable")
	}
	if isNullable(props["required_value"]) {
		t.Fatal("a value field must not be nullable")
	}

	// 必填性来自 binding:"required"，与可空性无关。
	required := schema.Required
	for _, want := range []string{"required_nullable", "required_value"} {
		if !slices.Contains(required, want) {
			t.Fatalf("%q is missing from required %v", want, required)
		}
	}
	for _, notWant := range []string{"optional_nullable", "optional_value"} {
		if slices.Contains(required, notWant) {
			t.Fatalf("%q must not be required", notWant)
		}
	}
}

// --- 3. time.Time 与自定义时间类型 ---

// customTime 是以 time.Time 为底层类型的自定义时间。
type customTime time.Time

type timeCases struct {
	Standard time.Time  `json:"standard"`
	Custom   customTime `json:"custom"`
	Optional *time.Time `json:"optional"`
}

// TestTimeTypesMapToDateTime 验证时间类型不被展开成私有字段。
//
// 反射看到的 time.Time 是一堆未导出字段，与线上的 RFC3339 字符串
// 毫无关系。展开的话，生成的客户端会去解析一个不存在的对象结构。
func TestTimeTypesMapToDateTime(t *testing.T) {
	t.Parallel()

	props := mustProperties(t, timeCases{})

	for _, name := range []string{"standard", "custom", "optional"} {
		s := props[name]
		if s.Format != "date-time" {
			t.Fatalf("%s format = %q, want date-time", name, s.Format)
		}
		if len(s.Properties) > 0 {
			t.Fatalf("%s was expanded into %d properties instead of a string", name, len(s.Properties))
		}
	}
	// optional 是指针，因此还应当可空。
	if !isNullable(props["optional"]) {
		t.Fatal("a *time.Time field must be nullable")
	}
}

// --- 4. 枚举 ---

// orderStatus 通过实现接口自述枚举取值。
//
// 用接口而不是 tag：tag 是侵入式的（要改 dto 的每个字段），
// 而接口只需在类型上实现一次，且**编译期**就能发现漏实现。
type orderStatus int8

func (orderStatus) SchemaEnumValues() []any { return []any{0, 1, 2} }

type enumCases struct {
	Status orderStatus `json:"status"`
}

// TestEnumTypesDeclareTheirValues 验证枚举产出 enum 而不是裸 integer。
//
// 裸 integer 意味着契约不说明 3 是不是合法值——客户端因此无法生成
// 有意义的校验，也无法生成类型安全的常量。
func TestEnumTypesDeclareTheirValues(t *testing.T) {
	t.Parallel()

	props := mustProperties(t, enumCases{})
	status := props["status"]

	if len(status.Enum) != 3 {
		t.Fatalf("status enum = %v, want three values", status.Enum)
	}
	if status.Type != "integer" {
		t.Fatalf("status type = %v, want the underlying integer type preserved", status.Type)
	}
}

// --- 5. 递归 / 自引用结构 ---

type commentNode struct {
	ID       int64          `json:"id"`
	Children []*commentNode `json:"children"`
	Parent   *commentNode   `json:"parent"`
}

// TestRecursiveStructsProduceRefs 钉住自引用产出 `$ref` 而不是无限展开。
//
// 就地展开会直接栈溢出；即便加了深度上限，产出的也是一份把同一结构
// 抄了 N 遍的文档，客户端生成器会为每一层各造一个类型。
func TestRecursiveStructsProduceRefs(t *testing.T) {
	t.Parallel()

	registry := NewSchemaRegistry()
	root, err := registry.Of(commentNode{}, RoleRequest)
	if err != nil {
		t.Fatalf("Of(commentNode): %v", err)
	}
	if root.Ref == "" {
		t.Fatal("a named struct must be referenced, not inlined")
	}

	components, err := registry.Components()
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	def, ok := components[strings.TrimPrefix(root.Ref, "#/components/schemas/")]
	if !ok {
		t.Fatalf("component %q was not registered", root.Ref)
	}

	// 自引用字段必须是引用。
	if def.Properties["parent"].Ref == "" {
		t.Fatal("the self-referencing field was inlined instead of referenced")
	}
	if def.Properties["children"].Items.Ref == "" {
		t.Fatal("the self-referencing slice element was inlined instead of referenced")
	}
}

// --- 6. 自定义 MarshalJSON ---

// money 序列化成字符串，与它的 Go 结构完全不同。
type money struct {
	Cents int64
}

func (m money) MarshalJSON() ([]byte, error) { return json.Marshal(m.String()) }
func (m money) String() string               { return "0.00" }

// OpenAPISchema 自述线上形状。
func (money) OpenAPISchema() *Schema {
	return &Schema{Type: "string", Description: "decimal amount rendered as a string"}
}

type marshalCases struct {
	Price money `json:"price"`
}

// TestCustomMarshalerUsesDeclaredShape 验证自定义序列化的类型用自述形状。
//
// 反射看到的是 `{Cents: int64}`，而线上是一个字符串。不给它自述的机会，
// 生成的 schema 会与真实响应不符——而那种不符只有客户端解析失败时才发现。
func TestCustomMarshalerUsesDeclaredShape(t *testing.T) {
	t.Parallel()

	props := mustProperties(t, marshalCases{})
	price := props["price"]

	if price.Type != "string" {
		t.Fatalf("price type = %v, want the declared string shape, not the Go struct", price.Type)
	}
	if len(price.Properties) > 0 {
		t.Fatal("price was expanded into Go fields instead of using its declared shape")
	}

	// 反向确认：这个类型确实有自定义序列化，所以上面的断言有意义。
	if !hasCustomMarshaler(reflectTypeOf(money{})) {
		t.Fatal("the fixture no longer has a custom marshaler; the assertion lost its meaning")
	}
}

// --- 7. any / interface{} 拒绝生成 ---

type anyCases struct {
	Payload any `json:"payload"`
}

type nestedAny struct {
	Inner anyCases `json:"inner"`
}

// TestInterfaceFieldsAreRejected 钉住本方案与通用库最重要的差别。
//
// 通用库对 interface{} 一律输出 `{}`——一个看起来有契约、实际什么也没
// 约束的 schema。它比没有 schema 更糟：审阅者以为契约完整，
// 而生成的客户端把该字段当成任意 JSON。
func TestInterfaceFieldsAreRejected(t *testing.T) {
	t.Parallel()

	registry := NewSchemaRegistry()

	_, err := registry.Of(anyCases{}, RoleRequest)
	if !errors.Is(err, ErrUnspecifiableType) {
		t.Fatalf("error = %v, want ErrUnspecifiableType", err)
	}
	if !strings.Contains(err.Error(), "payload") && !strings.Contains(err.Error(), "Payload") {
		t.Fatalf("error %q does not name the offending field", err)
	}

	// 嵌套结构里的 any 同样必须被拒——否则只要多包一层就能绕过。
	if _, err := NewSchemaRegistry().Of(nestedAny{}, RoleRequest); !errors.Is(err, ErrUnspecifiableType) {
		t.Fatalf("nested interface error = %v, want ErrUnspecifiableType", err)
	}
}

// --- 其余边界 ---

// TestUnsupportedKindsAreRejected 验证不可能出现在 JSON 里的类型被拒。
func TestUnsupportedKindsAreRejected(t *testing.T) {
	t.Parallel()

	type badMapKey struct {
		M map[int]string `json:"m"`
	}
	type badChan struct {
		C chan int `json:"c"`
	}

	for name, v := range map[string]any{"non-string map key": badMapKey{}, "channel": badChan{}} {
		if _, err := NewSchemaRegistry().Of(v, RoleRequest); !errors.Is(err, ErrUnspecifiableType) {
			t.Fatalf("%s: error = %v, want ErrUnspecifiableType", name, err)
		}
	}
}

// TestJSONDashFieldsAreSkipped 验证 `json:"-"` 的字段不进契约。
func TestJSONDashFieldsAreSkipped(t *testing.T) {
	t.Parallel()

	type withSkipped struct {
		Visible string `json:"visible"`
		Hidden  string `json:"-"`
		private string //nolint:unused // 未导出字段本就不该进契约，这里正是要验证它被跳过
	}

	props := mustProperties(t, withSkipped{})
	if _, ok := props["visible"]; !ok {
		t.Fatal("the visible field is missing")
	}
	if len(props) != 1 {
		t.Fatalf("properties = %v, want only the visible field", keysOfSchema(props))
	}
}

// TestNamedTypesAreNamespacedByPackage 验证 `$ref` 名字带包名。
//
// 两个包里同名的 Response 结构会互相覆盖，而那种覆盖是**静默的**——
// 生成的文档里只剩一个，另一个的字段全错。
func TestNamedTypesAreNamespacedByPackage(t *testing.T) {
	t.Parallel()

	registry := NewSchemaRegistry()
	ref, err := registry.Of(commentNode{}, RoleRequest)
	if err != nil {
		t.Fatalf("Of: %v", err)
	}
	if !strings.Contains(ref.Ref, "openapi.commentNode") {
		t.Fatalf("ref = %q, want it namespaced by package", ref.Ref)
	}
}

// TestRealDTOsGenerateCleanly 用仓库里真实的 DTO 跑一遍。
//
// 合成 fixture 全过但真实类型失败是常见情形：真实类型会带上
// 合成用例想不到的组合（内嵌、自定义类型、指针 + 校验 tag 并存）。
func TestRealDTOsGenerateCleanly(t *testing.T) {
	t.Parallel()

	for role, samples := range realDTOs() {
		registry := NewSchemaRegistry()
		for _, v := range samples {
			if _, err := registry.Of(v, role); err != nil {
				t.Fatalf("%T as %s: %v", v, role, err)
			}
		}
		if _, err := registry.Components(); err != nil {
			t.Fatalf("%s components: %v", role, err)
		}
	}
}

// TestRoleDecidesRequiredSemantics 钉住请求与响应的 required 判据不同。
//
// 用同一套规则的后果是单向的：套请求规则到响应上，响应字段**全部变成可选**，
// 调用方得为不可能缺失的字段写防御代码；反过来套则会让契约拒绝合法请求。
func TestRoleDecidesRequiredSemantics(t *testing.T) {
	t.Parallel()

	type both struct {
		Always   string `json:"always"`
		Optional string `json:"optional,omitempty"`
		Declared string `json:"declared" binding:"required"`
	}

	request := mustSchemaAs(t, both{}, RoleRequest)
	if !slices.Equal(request.Required, []string{"declared"}) {
		t.Fatalf("request required = %v, want only the binding-required field", request.Required)
	}

	response := mustSchemaAs(t, both{}, RoleResponse)
	if !slices.Equal(response.Required, []string{"always", "declared"}) {
		t.Fatalf("response required = %v, want every field without omitempty", response.Required)
	}
}

// TestSameTypeInBothRolesIsRejected 验证一个类型同时当请求体和响应体会被拒。
//
// 两种角色的 required 不同，任选一份都会让另一半的契约是错的；
// 静默取其一比报错危险得多，因为错的那半看起来完全正常。
func TestSameTypeInBothRolesIsRejected(t *testing.T) {
	t.Parallel()

	registry := NewSchemaRegistry()
	if _, err := registry.Of(tagged{}, RoleRequest); err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := registry.Of(tagged{}, RoleResponse); err != nil {
		t.Fatalf("response: %v", err)
	}
	if _, err := registry.Components(); !errors.Is(err, ErrUnspecifiableType) {
		t.Fatalf("Components error = %v, want the dual-role conflict to be reported", err)
	}
}

// TestUndeclaredCustomMarshalerIsRejected 验证自定义序列化必须自述形状。
//
// 不拒绝的话，契约会照着 Go 结构生成，而线上返回的是另一种东西——
// 一份看起来完整、实际处处对不上的契约。
func TestUndeclaredCustomMarshalerIsRejected(t *testing.T) {
	t.Parallel()

	type undeclared struct {
		Amount silentMoney `json:"amount"`
	}

	_, err := NewSchemaRegistry().Of(undeclared{}, RoleResponse)
	if !errors.Is(err, ErrUnspecifiableType) {
		t.Fatalf("error = %v, want ErrUnspecifiableType", err)
	}
	if !strings.Contains(err.Error(), "OpenAPISchema") {
		t.Fatalf("error %q does not tell the author how to fix it", err)
	}
}

// silentMoney 有自定义序列化但没有自述形状。
type silentMoney struct{ Cents int64 }

func (silentMoney) MarshalJSON() ([]byte, error) { return []byte(`"0.00"`), nil }

func mustSchema(t *testing.T, v any) *Schema {
	t.Helper()
	return mustSchemaAs(t, v, RoleRequest)
}

func mustSchemaAs(t *testing.T, v any, role Role) *Schema {
	t.Helper()

	registry := NewSchemaRegistry()
	ref, err := registry.Of(v, role)
	if err != nil {
		t.Fatalf("Of(%T): %v", v, err)
	}
	if ref.Ref == "" {
		return ref
	}
	components, err := registry.Components()
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	def, ok := components[strings.TrimPrefix(ref.Ref, "#/components/schemas/")]
	if !ok {
		t.Fatalf("component for %T was not registered", v)
	}
	return def
}

func mustProperties(t *testing.T, v any) map[string]*Schema {
	t.Helper()
	return mustSchema(t, v).Properties
}

// isNullable 报告 schema 的 type 是否含 "null"。
func isNullable(s *Schema) bool {
	list, ok := s.Type.([]any)
	if !ok {
		return false
	}
	return slices.Contains(list, any("null"))
}

func keysOfSchema(m map[string]*Schema) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
