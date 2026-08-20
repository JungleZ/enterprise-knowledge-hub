package services

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
)

// Seed creates a demo tenant with sample documents when the DB is empty.
func Seed(cfg *config.Config, ingest *IngestService) error {
	var count int64
	database.DB.Model(&models.Tenant{}).Count(&count)
	if count > 0 {
		return nil
	}

	fmt.Println("[seed] creating demo tenant ...")
	tenant := models.Tenant{Name: "星辰科技有限公司", Slug: "demo-tenant", Plan: "pro"}
	if err := database.DB.Create(&tenant).Error; err != nil {
		return err
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("demo123"), bcrypt.DefaultCost)

	admin := models.User{
		TenantID: tenant.ID, Email: "admin@demo.local", Name: "陈总(管理员)",
		Password: string(hash), Role: models.RoleSuperAdmin, Department: "全员", Title: "运营总监",
	}
	if err := database.DB.Create(&admin).Error; err != nil {
		return err
	}
	seedUser(tenant.ID, "finance@demo.local", "王小云", models.RoleMember, "财务部", "财务专员", "fin123")
	seedUser(tenant.ID, "rd@demo.local", "李工", models.RoleKnowledgeAdmin, "研发部", "技术主管", "rd123")
	seedUser(tenant.ID, "sales@demo.local", "销售小张", models.RoleMember, "销售部", "销售顾问", "sales123")

	kbs := []struct {
		name, desc, allowed string
	}{
		{"产品知识库", "产品参数、功能说明、售后与退款流程", ""},
		{"内部制度库", "人事、财务、差旅等内部制度(财务部限定)", "财务部,全员"},
		{"研发技术库", "API 文档与数据安全规范(研发部限定)", "研发部,全员"},
	}
	kbIDs := make([]uuid.UUID, 0, len(kbs))
	for _, k := range kbs {
		kb := models.KnowledgeBase{
			TenantID: tenant.ID, Name: k.name, Description: k.desc,
			AllowedDepartments: k.allowed, CreatorID: admin.ID,
		}
		if err := database.DB.Create(&kb).Error; err != nil {
			return err
		}
		kbIDs = append(kbIDs, kb.ID)
	}

	// sample documents (markdown content written to temp files then ingested)
	sampleDocs := []struct {
		kb       int
		title    string
		tags     string
		content  string
	}{
		{0, "产品A与产品B参数对比", "", productCompareDoc},
		{0, "客户退款流程", "", refundDoc},
		{1, "差旅报销制度", "财务部", travelDoc},
		{1, "员工考勤与请假制度", "财务部", attendanceDoc},
		{2, "API 接入文档", "研发部", apiDoc},
		{2, "数据安全与权限规范", "研发部", securityDoc},
	}
	for i, sd := range sampleDocs {
		filename := fmt.Sprintf("seed_%d.md", i)
		dir := filepath.Join(cfg.Storage.DocsPath, kbIDs[sd.kb].String())
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, []byte(sd.content), 0644); err != nil {
			return err
		}
		doc := models.Document{
			TenantID: tenant.ID, KBID: kbIDs[sd.kb], Title: sd.title,
			Filename: path, FileSize: int64(len(sd.content)), ContentType: "text/markdown",
			Status: models.DocStatusProcessing, AccessTags: sd.tags, CreatedBy: admin.ID,
		}
		if err := database.DB.Create(&doc).Error; err != nil {
			return err
		}
		if err := ingest.ProcessDocument(&doc); err != nil {
			fmt.Printf("[seed] ingest %q failed: %v\n", sd.title, err)
		}
	}

	fmt.Println("[seed] demo tenant ready. login: admin@demo.local / demo123")
	return nil
}

func seedUser(tenantID uuid.UUID, email, name, role, dept, title, pass string) {
	hash, _ := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	database.DB.Create(&models.User{
		TenantID: tenantID, Email: email, Name: name, Password: string(hash),
		Role: role, Department: dept, Title: title,
	})
}

const productCompareDoc = `# 产品A与产品B参数对比

## 一、定位差异
产品A定位企业级旗舰版，面向50人以上的成长型公司；产品B定位团队协作版，面向50人以下的微型团队。

## 二、核心参数对比
- 产品A：支持无限知识库、50GB存储、企业微信/钉钉/飞书机器人全接入、OCR图片识别、2万次/月问答、API开放接口、文档级权限管理。
- 产品B：仅支持3个知识库、5GB存储、仅网页端问答、1000次/月问答、基础权限。

## 三、价格
产品A订阅价 19999元/年，产品B订阅价 4999元/年。

## 四、选型建议
如果需要接入钉钉/企微机器人且对权限要求严格，建议选择产品A；如果只是小团队内部查询文档，产品B即可满足。
`

const refundDoc = `# 客户退款流程

## 适用范围
本流程适用于所有付费客户的退款申请。

## 退款步骤
1. 客户联系客服，说明退款原因，提供订单号。
2. 客服核实订单信息与剩余服务周期。
3. 若符合退款条件(合同期内、未发生违约)，客服提交退款申请单。
4. 财务部在3个工作日内完成退款审批。
5. 退款到账时间为审批通过后的1-3个工作日，原路退回支付账户。

## 退款比例
按剩余服务期比例退款，不足一个月的部分按一个月计算。

## 特别说明
以下情况不予退款：客户主动违约、已产生定制开发费用、购买超过12个月。
`

const travelDoc = `# 差旅报销制度

## 适用范围
全体员工出差期间的交通、住宿、餐饮费用报销。

## 报销标准
1. 高铁：二等座全报销，一等座需提前申请。
2. 住宿：一线城市每晚不超过500元，二线城市不超过400元。
3. 餐饮补贴：每天100元，无需发票。

## 报销流程
1. 出差结束后5个工作日内提交报销单。
2. 附上发票、行程单、审批通过的出差申请。
3. 财务部在7个工作日内完成审核打款。

## 常见问题
Q：打车费可以报销吗？ A：市内因公打车可报销，需附行程明细。
Q：周末出差算补贴吗？ A：出差期间均计入补贴，按实际天数计算。
`

const attendanceDoc = `# 员工考勤与请假制度

## 考勤时间
工作时间为周一至周五 9:00-18:00，午休1小时。

## 请假流程
1. 员工提前1天在OA系统提交请假申请。
2. 直属上级审批，3天以上需总监审批。
3. 病假需提供医院证明，可事后补交。

## 年假规定
入职满1年可享受5天年假，满3年可享受10天年假。

## 迟到早退
每月累计迟到3次以内免处罚，超过3次每次扣款100元。
`

const apiDoc = `# API 接入文档

## 认证方式
所有请求需要在 HTTP Header 中携带 Authorization: Bearer <access_token>。
Token 通过 OAuth 授权获取，有效期2小时。

## 常用接口
1. 创建知识库：POST /api/v1/kb
2. 上传文档：POST /api/v1/kb/{id}/documents
3. 发起问答：POST /api/v1/chat
4. 查询审计日志：GET /api/v1/admin/audit

## 限流策略
普通账号每秒最多10次请求，企业版账号每秒100次。

## 错误码
- 401：Token 无效或过期
- 403：权限不足
- 429：请求过于频繁
`

const securityDoc = `# 数据安全与权限规范

## 数据隔离原则
1. 不同企业(租户)的数据物理隔离，企业之间不可互相检索。
2. 部门之间通过权限标签隔离，市场部无法检索研发部文档。

## 权限等级
1. 公开(public)：全员可见。
2. 部门级：仅该部门成员可见。
3. 保密级：仅超级管理员可见。

## 敏感信息处理
身份证号、手机号、银行卡号等敏感信息在入库时自动脱敏处理。

## 审计要求
所有问答操作全程留痕，审计日志保存至少180天。
`
