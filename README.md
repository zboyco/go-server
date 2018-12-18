# go-server

1. 实现socket服务
简单实现一个socket服务,能接收客户端连接并接收数据

2. 循环读取数据
利用for循环,等待客户端发送数据

3. 利用goroutine实现同时多个客户端连接
将数据读取放入单独的方法中,利用goroutine运行