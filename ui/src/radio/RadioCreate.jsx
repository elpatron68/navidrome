import { useRef } from 'react'
import {
  Create,
  required,
  SimpleForm,
  TextInput,
  useTranslate,
} from 'react-admin'
import { Title } from '../common'
import { urlValidate } from '../utils/validations'
import { httpClient } from '../dataProvider'
import { REST_URL } from '../consts'
import RadioBrowserSearch from './RadioBrowserSearch'

const RadioTitle = () => {
  const translate = useTranslate()
  const resourceName = translate('resources.radio.name', {
    smart_count: 1,
  })
  const title = translate('ra.page.create', {
    name: `${resourceName}`,
  })
  return <Title subTitle={title} />
}

const RadioCreate = (props) => {
  const pendingFaviconRef = useRef('')

  const onSuccess = async (data) => {
    const faviconUrl = pendingFaviconRef.current
    pendingFaviconRef.current = ''
    if (!faviconUrl || !data?.id) {
      return
    }
    try {
      await httpClient(`${REST_URL}/radio/${data.id}/image/url`, {
        method: 'POST',
        body: JSON.stringify({ url: faviconUrl }),
        headers: new Headers({ 'Content-Type': 'application/json' }),
      })
    } catch (_e) {
      // Logo import is best-effort; station was created successfully
    }
  }

  return (
    <Create title={<RadioTitle />} {...props} onSuccess={onSuccess}>
      <SimpleForm redirect="list" variant={'outlined'}>
        <RadioBrowserSearch
          onFaviconSelected={(url) => {
            pendingFaviconRef.current = url
          }}
        />
        <TextInput source="name" validate={[required()]} />
        <TextInput
          type="url"
          source="streamUrl"
          fullWidth
          validate={[required(), urlValidate]}
        />
        <TextInput
          type="url"
          source="homePageUrl"
          fullWidth
          validate={[urlValidate]}
        />
      </SimpleForm>
    </Create>
  )
}

export default RadioCreate
