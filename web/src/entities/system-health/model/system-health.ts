/** 通过健康接口确认的后端运行快照。 */
export interface SystemHealthSnapshot {
  status: 'online'
  checkedAt: Date
}
