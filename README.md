# go-server

1. 实现socket服务  
    > 简单实现一个socket服务,能接收客户端连接并接收数据  
2. 循环读取数据  
    > 利用for循环,等待客户端发送数据  
3. 利用goroutine实现同时多个客户端连接  
    > 将数据读取放入单独的方法中,利用goroutine运行  
4. 将创建socket的方法放入单独的包中  
5. 将读取的数据处理方法作为参数传入server中  
    > OnMessage 和 OnError 通过Server结构公开变量传入  
6. 增加AppSession结构体  
    > OnMessage返回AppSession结构体,提供Send方法,服务器可以主动向客户端发送数据  
7. Session增加唯一ID,拆分socket中的Read方法  
    > 增加ID为了以后判断闲置超时;  
    拆分Read方法方便扩展协议  
8. session中Read方法实现粘包拆包处理  
    > 定义简单协议,数据包头由4字节构成:  
    > - 第1位固定为'$'  
    > - 第2-3位为Body长度(uint16)  
    > - 第4位固定为'#'  
    > - 接收数据时若第1位和第4位不正确则认为接收到异常数据,同时关闭socket连接  
9. 采用标准库scanner实现数据分离处理粘包  
    > 参考http://feixiao.github.io/2016/05/08/bufio/  
10. 添加Session容器,增加超时自动关闭Session功能  
11. 修改Session注册采用通道,避免新会话接入阻塞  
12. 优化部分代码，修改BUG  
    > 修改`sessionID`为string类型，采用UUID  
    超时会话关闭后从会话池中移除  
    暴露数据流财拆包规则，用户可以自定义拆包规则