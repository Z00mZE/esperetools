# esperetools

Набор утилит для разработки прошивок ESP32 на [TinyGo](https://tinygo.org/).
Компоненты находятся в отдельных папках и подключаются как Go-пакеты.

| Компонент | Назначение |
| --- | --- |
| [`kvstorage`](kvstorage/README.md) | Типизированное key-value хранилище во flash для строк, чисел, bool и слайсов; данные сохраняются после отключения питания |

```go
import "github.com/Z00mZE/esperetools/kvstorage"
```

Готовый пример для ESP32-S3:

```sh
GOFLAGS=-buildvcs=false tinygo flash -monitor -target=esp32s3-generic -serial=usb -port=COM6 ./kvstorage/examples/typed
```

Или `task flash-and-monitor`, если установлен Task. Команда использует `COM6`;
замените его на порт своей платы в `taskfile.yaml`.

Пример хранит значения разных типов в одной области и увеличивает счётчик запусков.
Для проверки сохранности отключите и снова подключите питание, затем
откройте монитор без повторной прошивки. Описание API, расположения данных и
проверки на плате — в [README компонента](kvstorage/README.md).

Проверка пакетов на компьютере:

```sh
go test ./...
go vet ./...
```
