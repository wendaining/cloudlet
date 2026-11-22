import type { FormRule } from 'antd';

export default [
  { required: true, message: '请输入项目名称' },
  { whitespace: true, message: '项目名称不能是空白' },
  { max: 100, message: '项目名称最多 100 个字符' },
] as FormRule[];
