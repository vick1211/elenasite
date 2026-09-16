package mailer

import (
	"elena-backend/internal/config"
	"fmt"
	"html"
	"time"

	gomail "gopkg.in/gomail.v2"
)

type Mailer struct {
	cfg config.SMTPConfig
}

func New(cfg config.SMTPConfig) *Mailer {
	return &Mailer{cfg: cfg}
}

const fontBody = `'Jost', Arial, Helvetica, sans-serif`
const fontHeading = `'Playfair Display', Georgia, 'Times New Roman', serif`
const colorPrimary = "#385C8E"
const colorBg = "#FAF8F4"
const colorText = "#2B2B2B"
const colorTextMuted = "#6A6A6A"

func emailShell(preheader, bodyHTML string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Елена Костарева</title>
<style>
  @import url('https://fonts.googleapis.com/css2?family=Playfair+Display:wght@600;700&family=Jost:wght@400;500;600&display=swap');
  body { margin:0; padding:0; background-color:%s; }
  a { color:%s; }
</style>
</head>
<body style="margin:0; padding:0; background-color:%s;">
  <div style="display:none; max-height:0; overflow:hidden; opacity:0;">%s</div>
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:%s; padding:32px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="max-width:560px; background-color:#FFFFFF; border:1px solid %s;">
          <tr>
            <td style="background-color:%s; padding:28px 32px; text-align:center;">
              <div style="font-family:%s; font-size:22px; font-weight:700; color:#FFFFFF; letter-spacing:0.3px;">Елена Костарева</div>
              <div style="font-family:%s; font-size:11px; color:#DCE4F0; margin-top:6px; letter-spacing:1.5px; text-transform:uppercase;">Психолог &middot; Коуч</div>
            </td>
          </tr>
          <tr>
            <td style="padding:32px;">
              %s
            </td>
          </tr>
          <tr>
            <td style="padding:20px 32px; border-top:1px solid #EDE8DF; background-color:%s;">
              <div style="font-family:%s; font-size:12px; color:#8A8A8A; line-height:1.6;">
                Костарева Елена Николаевна &middot; ИНН 666300057691<br>
                <a href="mailto:e-kosta@yandex.ru" style="color:%s; text-decoration:none;">e-kosta@yandex.ru</a>
              </div>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`,
		colorBg, colorPrimary, colorBg, html.EscapeString(preheader), colorBg,
		colorPrimary, colorPrimary, fontHeading, fontBody, bodyHTML,
		colorBg, fontBody, colorPrimary,
	)
}

func emailButton(href, label string) string {
	return fmt.Sprintf(`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:24px 0 4px;">
  <tr>
    <td style="background-color:%s;">
      <a href="%s" style="display:inline-block; padding:14px 28px; font-family:%s; font-size:14px; font-weight:500; color:#FFFFFF; text-decoration:none; letter-spacing:0.3px;">%s</a>
    </td>
  </tr>
</table>`, colorPrimary, html.EscapeString(href), fontBody, html.EscapeString(label))
}

func infoBox(innerHTML string) string {
	return fmt.Sprintf(`<table role="presentation" cellpadding="0" cellspacing="0" style="width:100%%; margin:20px 0; background-color:%s; border-left:3px solid %s;">
  <tr>
    <td style="padding:16px 20px; font-family:%s; font-size:15px; color:%s;">%s</td>
  </tr>
</table>`, colorBg, colorPrimary, fontBody, colorText, innerHTML)
}

type BookingData struct {
	FirstName       string
	AppointmentDate time.Time
	CancelURL       string
}

func (m *Mailer) SendBookingConfirmation(to string, d BookingData) error {
	date := d.AppointmentDate.Format("02.01.2006 в 15:04")

	subject := fmt.Sprintf("%s, вы записаны на приём %s к Елене", d.FirstName, date)

	plainText := fmt.Sprintf(
		"%s, вы записаны на приём %s к Елене.\n\n"+
			"Вы можете отменить запись по ссылке:\n%s",
		d.FirstName, date, d.CancelURL,
	)

	body := fmt.Sprintf(`
		<div style="font-family:%s; font-size:20px; font-weight:600; color:%s; margin-bottom:16px;">Вы записаны на приём</div>
		<div style="font-family:%s; font-size:15px; color:%s; line-height:1.7;">
			%s, здравствуйте!<br><br>
			Подтверждаем запись на приём к Елене Костаревой.
		</div>
		%s
		<div style="font-family:%s; font-size:14px; color:%s; line-height:1.7;">
			Если планы изменятся, вы можете отменить запись по кнопке ниже.
		</div>
		%s
	`,
		fontHeading, colorText,
		fontBody, colorTextMuted, html.EscapeString(d.FirstName),
		infoBox(fmt.Sprintf("<strong>Дата и время:</strong> %s", html.EscapeString(date))),
		fontBody, colorTextMuted,
		emailButton(d.CancelURL, "Отменить запись"),
	)

	htmlBody := emailShell(fmt.Sprintf("Вы записаны на приём %s к Елене", date), body)

	return m.send(to, subject, plainText, htmlBody)
}

type RescheduleData struct {
	FirstName string
	OldDate   time.Time
	NewDate   time.Time
	CancelURL string
}

func (m *Mailer) SendRescheduleNotice(to string, d RescheduleData) error {
	oldDate := d.OldDate.Format("02.01.2006 в 15:04")
	newDate := d.NewDate.Format("02.01.2006 в 15:04")

	subject := fmt.Sprintf("%s, ваша запись перенесена на %s", d.FirstName, newDate)

	plainText := fmt.Sprintf(
		"%s, ваша запись к Елене перенесена с %s на %s.\n\n"+
			"Вы можете отменить запись по ссылке:\n%s",
		d.FirstName, oldDate, newDate, d.CancelURL,
	)

	body := fmt.Sprintf(`
		<div style="font-family:%s; font-size:20px; font-weight:600; color:%s; margin-bottom:16px;">Запись перенесена</div>
		<div style="font-family:%s; font-size:15px; color:%s; line-height:1.7;">
			%s, здравствуйте!<br><br>
			Ваша запись к Елене Костаревой перенесена с <strong>%s</strong> на новое время.
		</div>
		%s
		<div style="font-family:%s; font-size:14px; color:%s; line-height:1.7;">
			Если новое время вам не подходит, вы можете отменить запись по кнопке ниже.
		</div>
		%s
	`,
		fontHeading, colorText,
		fontBody, colorTextMuted, html.EscapeString(d.FirstName), html.EscapeString(oldDate),
		infoBox(fmt.Sprintf("<strong>Новая дата и время:</strong> %s", html.EscapeString(newDate))),
		fontBody, colorTextMuted,
		emailButton(d.CancelURL, "Отменить запись"),
	)

	htmlBody := emailShell(fmt.Sprintf("Ваша запись перенесена на %s", newDate), body)

	return m.send(to, subject, plainText, htmlBody)
}

type CancellationData struct {
	FirstName       string
	AppointmentDate time.Time
	Refund          bool
	DeadlineHours   int
	StaffInitiated  bool
}

func (m *Mailer) SendCancellationConfirmation(to string, d CancellationData) error {
	date := d.AppointmentDate.Format("02.01.2006 в 15:04")

	var refundNote string
	if d.Refund {
		refundNote = "Оплата будет возвращена в течение нескольких рабочих дней."
	} else {
		refundNote = fmt.Sprintf(
			"К сожалению, отмена произошла менее чем за %d ч. до приёма — средства не возвращаются.",
			d.DeadlineHours,
		)
	}

	var subject, introPlain, introHTML string
	if d.StaffInitiated {
		subject = "Ваша запись к Елене отменена"
		introPlain = fmt.Sprintf("Запись на приём %s к Елене была отменена.", date)
		introHTML = fmt.Sprintf("Запись на приём <strong>%s</strong> к Елене Костаревой была отменена.", html.EscapeString(date))
	} else {
		subject = fmt.Sprintf("%s, вы отменили запись к Елене", d.FirstName)
		introPlain = fmt.Sprintf("Вы отменили запись на приём %s к Елене.", date)
		introHTML = fmt.Sprintf("Вы отменили запись на приём <strong>%s</strong> к Елене Костаревой.", html.EscapeString(date))
	}

	plainText := fmt.Sprintf("%s\n\n%s", introPlain, refundNote)

	body := fmt.Sprintf(`
		<div style="font-family:%s; font-size:20px; font-weight:600; color:%s; margin-bottom:16px;">Запись отменена</div>
		<div style="font-family:%s; font-size:15px; color:%s; line-height:1.7;">
			%s
		</div>
		%s
	`,
		fontHeading, colorText,
		fontBody, colorTextMuted, introHTML,
		infoBox(html.EscapeString(refundNote)),
	)

	htmlBody := emailShell("Запись отменена", body)

	return m.send(to, subject, plainText, htmlBody)
}

func (m *Mailer) SendPaymentFailedNotice(to, firstName string) error {
	subject := "Не удалось провести оплату"

	plainText := fmt.Sprintf(
		"%s, к сожалению, оплата не прошла, и бронь времени была отменена.\n\n"+
			"Вы можете повторить попытку записи на сайте.",
		firstName,
	)

	body := fmt.Sprintf(`
		<div style="font-family:%s; font-size:20px; font-weight:600; color:%s; margin-bottom:16px;">Оплата не прошла</div>
		<div style="font-family:%s; font-size:15px; color:%s; line-height:1.7;">
			%s, к сожалению, оплата не была завершена, и бронь времени была автоматически отменена.<br><br>
			Вы можете повторить попытку записи на сайте.
		</div>
		%s
	`,
		fontHeading, colorText,
		fontBody, colorTextMuted, html.EscapeString(firstName),
		emailButton("https://ekosta.ru/", "Записаться снова"),
	)

	htmlBody := emailShell("Оплата не прошла", body)

	return m.send(to, subject, plainText, htmlBody)
}

func (m *Mailer) send(to, subject, plainText, htmlBody string) error {
	msg := gomail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", to)
	msg.SetHeader("Subject", subject)
	msg.SetBody("text/plain", plainText)
	msg.AddAlternative("text/html", htmlBody)

	d := gomail.NewDialer(m.cfg.Host, m.cfg.Port, m.cfg.User, m.cfg.Password)
	d.SSL = m.cfg.Port == 465

	if err := d.DialAndSend(msg); err != nil {
		return fmt.Errorf("mailer: %w", err)
	}
	return nil
}

type StubMailer struct{}

func (s *StubMailer) SendBookingConfirmation(_ string, d BookingData) error {
	fmt.Printf("[STUB MAIL] booking: to=%s date=%s cancel=%s\n",
		"<email>", d.AppointmentDate.Format(time.RFC3339), d.CancelURL)
	return nil
}

func (s *StubMailer) SendRescheduleNotice(_ string, d RescheduleData) error {
	fmt.Printf("[STUB MAIL] reschedule: old=%s new=%s cancel=%s\n",
		d.OldDate.Format(time.RFC3339), d.NewDate.Format(time.RFC3339), d.CancelURL)
	return nil
}

func (s *StubMailer) SendCancellationConfirmation(_ string, d CancellationData) error {
	fmt.Printf("[STUB MAIL] cancellation: date=%s refund=%v deadline_hours=%d\n",
		d.AppointmentDate.Format(time.RFC3339), d.Refund, d.DeadlineHours)
	return nil
}

func (s *StubMailer) SendPaymentFailedNotice(to, firstName string) error {
	fmt.Printf("[STUB MAIL] payment failed: to=%s name=%s\n", to, firstName)
	return nil
}
