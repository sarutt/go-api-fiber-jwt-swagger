# เอกสารออกแบบระบบ (Design)

สถานะ: ร่างเพื่อให้ตรวจ — อ้างอิงความต้องการใน `requirements.md`

## 1. ทางเลือกที่ตกลงแล้ว
- Backend: Go Fiber, modular monolith + PostgreSQL (ต่อยอดโปรเจกต์เดิม: JWT + Swagger)
- Frontend: เว็บ responsive (PWA) ด้วย React + Vite + TypeScript, เมนูแยกตามบทบาท
- ภาษา UI: ไทยอย่างเดียว (ข้อสมมติ — รองรับ i18n ภายหลังได้)

## 2. ข้อสมมติ (แก้ได้)
- ผู้ใช้ต้องมีอีเมลหรือเบอร์โทรอย่างน้อยหนึ่งอย่าง ใช้ล็อกอิน
- ดอกเบี้ยคงที่: ดอกเบี้ยรวม = เงินต้นที่ผ่อน x อัตราต่อปี x จำนวนเดือน / 12 (เงินต้นที่ผ่อน = ราคาผ่อน - เงินดาวน์) ค่างวด = (เงินต้น + ดอกเบี้ยรวม) / จำนวนงวด ปัดเศษขึ้นเป็นสตางค์ งวดสุดท้ายรับส่วนต่าง
- ค่าดอกเบี้ย ค่าปรับ วันครบกำหนด เก็บในสัญญาแต่ละฉบับ (snapshot ณ วันสร้าง ไม่เปลี่ยนตามค่ากลาง)
- ตารางงวดคำนวณและบันทึกเมื่อสัญญาได้รับอนุมัติ
- ยอดขายนับเมื่อสัญญาอนุมัติแล้วเท่านั้น
- เงินเก็บเป็นจำนวนเต็มสตางค์ (หรือ NUMERIC(14,2)) ไม่ใช้ float
- ไม่เก็บเลขบัตรเครดิตเอง ใช้ token จาก payment gateway เท่านั้น
- Payment gateway และระบบตรวจสลิปอยู่หลัง interface กลาง เลือกเจ้าทีหลัง

## 3. บทบาทและสิทธิ์
| บทบาท | สิทธิ์ |
|---|---|
| customer | ดูสัญญา/งวด/ยอดค้างของตัวเอง, ชำระเงิน, แนบสลิป, ผูก/ถอดบัตร, เปลี่ยนรหัสผ่าน, ดูแจ้งเตือน |
| sales (ลูกทีม) | สร้างบัญชีลูกค้า, สร้างสัญญา, ดูสินค้าคงเหลือ, ดูเฉพาะลูกค้า/สัญญาที่ตัวเองขาย |
| manager (หัวหน้า) | ทุกอย่างของ sales ในทุกทีม, อนุมัติ/ปฏิเสธสัญญา, จัดการสินค้าและคลัง, ตั้งค่า, ดูรายงานทั้งหมด |

หลักการ: ตรวจสิทธิ์ที่ชั้น service โดยกรองด้วย `sales_id` ของผู้ใช้ ไม่ไว้ใจ client

## 4. โครงสร้าง module (Go)
```
cmd/server/main.go
internal/
  auth/          ล็อกอิน JWT, เปลี่ยนรหัสผ่าน, middleware สิทธิ์
  users/         ผู้ใช้ ลูกค้า ทีม
  catalog/       สินค้า หมวด รูป
  inventory/     สต็อกคลังเดียว + stock_movements
  contracts/     สัญญา การอนุมัติ คำนวณตารางงวด
  billing/       งวดชำระ ค่าปรับ สถานะค้างชำระ
  payments/      การชำระ, สลิป, บัตรที่ผูก (ผ่าน gateway interface)
  notifications/ แจ้งในแอป + อีเมล, กติกาเตือนล่วงหน้า
  reports/       ยอดขายต่อคน ค้างชำระ ยอดเก็บรายเดือน สินค้าใกล้หมด
  jobs/          scheduler: ตัดบัตร, เตือนล่วงหน้า, ตีสถานะค้าง
  platform/      db, config, logger, gateway/slip/email interfaces
```
กติกา: module เรียกกันผ่าน interface ของ service เท่านั้น ห้ามอ่านตารางของ module อื่นตรงๆ

## 5. Data model (PostgreSQL)
- `users` (id, role, email?, phone?, password_hash, full_name, is_active, must_change_password)
- `teams` (id, name), `team_members` (team_id, user_id)
- `customers` (id, user_id, sales_id → users, national_id?, address, created_at)
- `categories` (id, name)
- `products` (id, sku UNIQUE, name, category_id, cash_price, installment_price, image_url, low_stock_threshold, is_active)
- `stock_levels` (product_id PK, quantity CHECK >= 0)
- `stock_movements` (id, product_id, type[receive|adjust|transfer|contract_out|contract_release], qty_delta, ref_type, ref_id, note, created_by, created_at)
- `contracts` (id, contract_no, customer_id, sales_id, product_id, type[cash|installment], status, price, down_payment, interest_rate_pct_year, installments_count, installment_amount, billing_day, late_fee_enabled, late_fee_amount, approved_by, approved_at, reject_reason, created_at)
- `installments` (id, contract_id, seq, due_date, amount_due, late_fee, amount_paid, status[pending|paid|overdue|waiting_review], paid_at)
- `payments` (id, contract_id, installment_id?, method[qr|card|transfer_slip|card_autocharge], amount, status[pending|succeeded|failed|rejected], gateway_ref, slip_id?, created_at)
- `payment_slips` (id, payment_id, image_url, verification_status, verified_amount, raw_response)
- `payment_methods` (id, customer_id, gateway_customer_ref, gateway_card_token, brand, last4, is_default)
- `notification_rules` (id, days_before, channels[email,in_app]) — ตั้งค่าได้
- `notifications` (id, user_id, channel, type, title, body, read_at, sent_at)
- `settings` (key, value jsonb) — ค่ากลางที่เป็นค่าเริ่มต้นตอนสร้างสัญญา
- `audit_log` (id, actor_id, action, entity, entity_id, diff, created_at)

ข้อจำกัดสำคัญ: `stock_levels.quantity >= 0`, `installments (contract_id, seq)` UNIQUE, ดัชนีบน `customers.sales_id`, `contracts.sales_id`, `installments (due_date, status)`

## 6. สถานะสัญญา
```
draft? -> pending_approval -> approved/active -> completed
                          \-> rejected (คืนสต็อก)
active -> overdue (มีงวดค้าง) -> active (ชำระครบงวดที่ค้าง) | defaulted (เฟสถัดไป)
active/pending_approval -> cancelled (คืนสต็อก, เฉพาะหัวหน้า)
```
- สร้างสัญญา: ใน transaction เดียว ตัดสต็อก (`UPDATE ... WHERE quantity >= 1`) + บันทึก movement `contract_out` + สถานะ `pending_approval`
- ปฏิเสธ/ยกเลิก: ใน transaction เดียว คืนสต็อก + movement `contract_release`
- อนุมัติ: สร้างตารางงวด (สัญญาผ่อน) หรืองวดเดียวเต็มจำนวน (ขายสด)

## 7. สถานะงวดและการชำระ
- ชำระผ่าน QR/บัตร: gateway เรียก webhook ยืนยัน -> บันทึกชำระ (idempotent ด้วย `gateway_ref`)
- โอนแนบสลิป: สร้าง payment `pending` -> ระบบตรวจสลิปอัตโนมัติ -> ผ่าน = `succeeded`, ไม่ผ่าน = `rejected` + แจ้งลูกค้า; ตรวจซ้ำสลิปเดิมไม่ได้ (กันสลิปซ้ำ)
- ตัดบัตรอัตโนมัติ (job รายวัน): งวดถึง `due_date` ตัดด้วย token เริ่มต้น; ไม่ผ่าน -> งวด `overdue`, สัญญา `overdue`, แจ้งลูกค้าและหัวหน้า
- ค่าปรับ: ถ้า `late_fee_enabled` เมื่องวดเกินกำหนดบวก `late_fee_amount` (ครั้งเดียวต่องวด — ข้อสมมติ)
- การตัดเงิน ลองตัดซ้ำได้กี่ครั้ง/ห่างกี่วัน: ยังไม่กำหนด (ดูคำถามค้าง)

## 8. API หลัก (REST, prefix `/api/v1`, ทั้งหมดอธิบายด้วย Swagger)
- auth: `POST /auth/login`, `POST /auth/change-password`, `POST /auth/refresh`
- users/customers: `POST /customers` (sales), `GET /customers` (กรองตามสิทธิ์), `GET /customers/{id}`
- catalog: `GET/POST/PUT /products`, `GET /categories`
- inventory: `GET /inventory`, `POST /inventory/receive|adjust|transfer` (manager), `GET /inventory/movements`
- contracts: `POST /contracts`, `GET /contracts`, `GET /contracts/{id}`, `POST /contracts/{id}/approve|reject|cancel` (manager)
- billing/me: `GET /me/contracts`, `GET /me/installments`, `GET /me/summary` (ยอดที่ต้องชำระ)
- payments: `POST /payments/qr`, `POST /payments/card`, `POST /payments/slip`, `POST /webhooks/gateway`
- cards: `GET/POST/DELETE /me/payment-methods`
- notifications: `GET /me/notifications`, `PATCH /me/notifications/{id}/read`
- settings: `GET/PUT /settings/notification-rules`, `GET/PUT /settings/defaults` (manager)
- reports (manager): `GET /reports/sales-by-user`, `/reports/overdue`, `/reports/monthly-collection`, `/reports/low-stock`

## 9. Background jobs
| job | ความถี่ | หน้าที่ |
|---|---|---|
| reminders | รายวัน | สร้างแจ้งเตือนงวดที่ใกล้ครบกำหนดตาม `notification_rules` |
| autocharge | รายวัน | ตัดบัตรงวดที่ครบกำหนด (idempotent ต่อ installment) |
| overdue | รายวัน | เปลี่ยนงวด/สัญญาเป็นค้างชำระ + คิดค่าปรับ + แจ้งหัวหน้า |
| mail-sender | ต่อเนื่อง | ส่งอีเมลจากคิว `notifications` |
ทุก job ต้อง idempotent และใช้ advisory lock กันรันซ้อน

## 10. หน้าจอ (เว็บ)
- ลูกค้า: ภาพรวมยอดค้าง, รายการสัญญา/งวด, ชำระเงิน (QR/บัตร/สลิป), จัดการบัตร, แจ้งเตือน, เปลี่ยนรหัสผ่าน
- ลูกทีม: สินค้าคงเหลือ, สร้างลูกค้า, สร้างสัญญา, รายการสัญญาของฉัน
- หัวหน้า: คิวอนุมัติสัญญา, คลังสินค้าและประวัติ, ทีม/ยอดขาย, รายงาน 4 รายการ, ตั้งค่า

## 11. ความปลอดภัย
- รหัสผ่าน bcrypt/argon2, JWT อายุสั้น + refresh, rate limit ที่ล็อกอิน
- webhook ตรวจ signature, เก็บ raw payload
- เก็บ audit log ของการอนุมัติ ปรับสต็อก และตั้งค่า
- PII (เลขบัตรประชาชน) เข้ารหัสในคอลัมน์ หรือไม่เก็บถ้าไม่จำเป็น

## 12. แผนการสร้าง (เฟส)
1. รากฐาน: โครงสร้างโปรเจกต์, PostgreSQL + migration, auth + บทบาท
2. สินค้า/คลัง + movement log
3. ลูกค้า + สัญญา (สร้าง/อนุมัติ/ปฏิเสธ + ตัด/คืนสต็อก + ตารางงวด)
4. การชำระเงิน (gateway interface, สลิป, ตัดบัตรอัตโนมัติ) + job ค้างชำระ
5. แจ้งเตือน (ในแอป + อีเมล) + รายงาน
6. เว็บ frontend (ทำคู่ไปกับแต่ละเฟส)

## 13. คำถามที่ยังเปิดอยู่
- ตัดบัตรไม่ผ่านจะลองใหม่กี่ครั้ง ห่างกันกี่วัน (ก่อนสถานะค้างชำระ)
- ค่าปรับคิดครั้งเดียวต่องวด หรือคิดรายวัน
- ดอกเบี้ยคงที่เป็นอัตรา % ต่อปีหรือต่อเดือน
- ขายสด: จ่ายเต็มจำนวนครั้งเดียวเมื่อไร (ก่อน/หลังอนุมัติ)
- ลูกค้าปิดยอดก่อนกำหนดได้ไหม (ส่วนลดดอกเบี้ย?)
- คอมมิชชันของลูกทีม (นอกขอบเขตรอบแรกหรือไม่)
- ผู้ให้บริการ gateway / ตรวจสลิป / อีเมลที่จะใช้
