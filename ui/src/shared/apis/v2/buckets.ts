import {Bucket} from 'src/api'
import {bucketsAPI} from 'src/utils/api'

export const getBuckets = async (): Promise<Bucket[]> => {
  try {
    const {data} = await bucketsAPI.bucketsGet('')

    return data.buckets
  } catch (error) {
    console.error(error)
    throw error
  }
}
