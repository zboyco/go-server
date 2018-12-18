# go-server

1. 实现socket服务

    简单实现一个socket服务,能接收客户端连接并接收数据

2. 循环读取数据

    利用for循环,等待客户端发送数据

3. 利用goroutine实现同时多个客户端连接

    将数据读取放入单独的方法中,利用goroutine运行

4. 将创建socket的方法放入单独的包中

5. 将读取的数据处理方法作为参数传入server中

    OnMessage 和 OnError 通过Server结构公开变量传入

6. 增加AppSession结构体

    OnMessage返回AppSession结构体,提供Send方法,服务器可以主动向客户端发送数据

7. Session增加唯一ID,拆分socket中的Read方法

    增加ID为了以后判断闲置超时;
    拆分Read方法方便扩展协议