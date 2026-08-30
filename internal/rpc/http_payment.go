package rpc

import (
	"html/template"
	"net/http"
)

// Страница, которая имитирует ввод карты и возвращает credentials в Telegram
var paymentHTML = `
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Dev Payment Stars</title>
    <script src="https://telegram.org/js/telegram-web-app.js"></script>
    <style>
        body { font-family: sans-serif; display: flex; justify-content: center; align-items: center; height: 100vh; background: #f4f4f9; }
        .box { background: white; padding: 20px; border-radius: 10px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; }
        button { background: #248bcf; color: white; border: none; padding: 10px 20px; border-radius: 5px; cursor: pointer; }
    </style>
</head>
<body>
    <div class="box">
        <h2>Страница оплаты</h2>
        <p>Нажмите кнопку ниже, чтобы подтвердить оплату.</p>
        <button onclick="pay()">Подтвердить оплату</button>
    </div>
    <script>
        function pay() {
            const urlParams = new URLSearchParams(window.location.search);
            const formId = urlParams.get('form_id');
            
            // Данные, которые ожидает функция validDevStarsPaymentCredentials в вашем Go коде
            const payload = JSON.stringify({
                "type": "telesrv_dev",
                "form_id": formId
            });

            // Отправляем данные клиенту Telegram
            if (window.TelegramWebviewProxy) {
                window.TelegramWebviewProxy.postEvent('payment_form_submit', JSON.stringify({
                    "credentials": {
                        "type": "card",
                        "data": payload
                    }
                }));
            }
        }
    </script>
</body>
</html>`

// HandlePaymentForm — это стандартный http.HandlerFunc
func (r *Router) HandlePaymentForm(w http.ResponseWriter, req *http.Request) {
	tmpl := template.Must(template.New("payment").Parse(paymentHTML))
	tmpl.Execute(w, nil)
}