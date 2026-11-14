# esperetools

Набор утилит для разработки прошивок ESP32 на [TinyGo](https://tinygo.org/).
Компоненты находятся в отдельных папках и подключаются как Go-пакеты.

| Компонент | Назначение |
| --- | --- |
| [`kvstorage`](kvstorage/README.md) | Key-value хранилище во flash; данные сохраняются после отключения питания |

```go
import "github.com/Z00mZE/esperetools/kvstorage"
```

Готовый пример для ESP32-S3:

```sh
GOFLAGS=-buildvcs=false tinygo flash -monitor -target=esp32s3-generic -serial=usb -port=COM6 ./kvstorage/examples/basic
```

Или `task flash-and-monitor`, если установлен Task. Команда использует `COM6`;
замените его на порт своей платы в `taskfile.yaml`.

Пример сначала читает значение из flash и записывает его только при первом
запуске. Для проверки сохранности отключите и снова подключите питание, затем
откройте монитор без повторной прошивки. Описание API, расположения данных и
проверки на плате — в [README компонента](kvstorage/README.md).

Проверка пакетов на компьютере:

```sh
go test ./...
go vet ./...
```
